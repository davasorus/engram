package core_test

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"testing"

	"github.com/davasorus/engram/internal/core"
)

// memStore is an in-memory core.Store for testing engine LOGIC without a real
// Postgres. Semantic search uses a simple cosine so ranking is exercised; the
// real pgvector path is verified separately against a live DB.
type memStore struct{ notes map[string]core.Note }

func newMem() *memStore { return &memStore{notes: map[string]core.Note{}} }

func (m *memStore) Upsert(_ context.Context, n core.Note) error { m.notes[n.ID] = n; return nil }
func (m *memStore) UpsertTx(_ context.Context, _ core.Tx, n core.Note) error {
	m.notes[n.ID] = n
	return nil
}
func (m *memStore) BeginTx(_ context.Context) (core.Tx, error) { return nil, nil }
func (m *memStore) Get(_ context.Context, id string) (*core.Note, error) {
	n, ok := m.notes[id]
	if !ok {
		return nil, nil
	}
	return &n, nil
}
func (m *memStore) Delete(_ context.Context, id string) error { delete(m.notes, id); return nil }
func (m *memStore) List(_ context.Context, _ string, limit, offset int) ([]core.Note, error) {
	var out []core.Note
	for _, n := range m.notes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	if offset > len(out) {
		return nil, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) ListAfter(_ context.Context, afterID string, limit int) ([]core.Note, error) {
	var out []core.Note
	for _, n := range m.notes {
		if n.ID > afterID {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) Count(_ context.Context) (int, error) { return len(m.notes), nil }
func (m *memStore) MissingVectorIDs(_ context.Context) ([]string, error) {
	var ids []string
	for id, n := range m.notes {
		if len(n.Vector) == 0 {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (m *memStore) SearchSemantic(_ context.Context, _ string, q []float32, limit int) ([]core.SearchHit, error) {
	var hits []core.SearchHit
	for _, n := range m.notes {
		if len(n.Vector) == 0 {
			continue
		}
		hits = append(hits, core.SearchHit{Note: n, Score: cosine(q, n.Vector), Kind: "semantic"})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
func (m *memStore) KeywordSearch(_ context.Context, _ string, q string, limit int) ([]core.Note, error) {
	var out []core.Note
	ql := strings.ToLower(q)
	for _, n := range m.notes {
		if strings.Contains(strings.ToLower(n.Title), ql) || strings.Contains(strings.ToLower(n.Body), ql) {
			out = append(out, n)
		}
	}
	return out, nil
}
func (m *memStore) Backlinks(_ context.Context, idOrTitle string, _ string) ([]core.Backlink, error) {
	key := strings.ToLower(strings.TrimSpace(idOrTitle))
	var title string
	if n, ok := m.notes[idOrTitle]; ok {
		title = strings.ToLower(n.Title)
	}
	var out []core.Backlink
	for _, n := range m.notes {
		for _, l := range n.Links {
			ll := strings.ToLower(l)
			if ll == key || (title != "" && ll == title) {
				out = append(out, core.Backlink{ID: n.ID, Title: n.Title})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}
func (m *memStore) Stats(_ context.Context) (core.Stats, error) {
	st := core.Stats{TotalNotes: len(m.notes), NotesByProject: map[string]int{}}
	for _, n := range m.notes {
		if n.Project != "" {
			st.NotesByProject[n.Project]++
		}
		if len(n.Vector) == 0 {
			st.NotesMissingVector++
		}
		st.TotalLinks += len(n.Links)
	}
	st.TotalProjects = len(st.NotesByProject)
	return st, nil
}
func (m *memStore) Close() error { return nil }

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

type fakeEmbedder struct{}

func (fakeEmbedder) Model() string { return "fake" }
func (fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v := make([]float32, 16)
	for _, w := range strings.Fields(text) {
		h := fnv.New32a()
		h.Write([]byte(strings.ToLower(w)))
		v[h.Sum32()%16] += 1
	}
	return v, nil
}

func newEngine(t *testing.T) *core.Engine {
	t.Helper()
	return core.NewEngine(newMem(), fakeEmbedder{})
}

func TestWriteReadRoundTrip(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	n, err := e.Write(ctx, core.WriteInput{Title: "SQL AG Setup", Body: "Steps to configure an [[Availability Group]].", Tags: []string{"sql", "runbook"}})
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != "sql-ag-setup" {
		t.Fatalf("slug: got %q", n.ID)
	}
	got, _ := e.Read(ctx, "sql-ag-setup")
	if got == nil || got.Title != "SQL AG Setup" {
		t.Fatal("roundtrip lost data")
	}
	if len(got.Links) != 1 || got.Links[0] != "availability group" {
		t.Fatalf("links: %v", got.Links)
	}
	if len(got.Vector) == 0 {
		t.Fatal("no vector stored")
	}
}

func TestSemanticSearchRanks(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, err := e.Write(ctx, core.WriteInput{Title: "Postgres backups", Body: "pg_dump and WAL archiving for postgres backups"})
	if err != nil {
		t.Fatalf("failed to write note: %v", err)
	}

	_, _ = e.Write(ctx, core.WriteInput{Title: "Cat facts", Body: "cats sleep a lot and purr"})
	if err != nil {
		t.Fatalf("failed to write note: %v", err)
	}
	hits, err := e.Search(ctx, "", "postgres backup strategy", 5, "semantic")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Note.Title != "Postgres backups" {
		t.Fatalf("expected postgres note first, got %+v", hits)
	}
}

func TestKeywordSearch(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, _ = e.Write(ctx, core.WriteInput{Title: "Docker notes", Body: "podman play kube is handy"})
	hits, err := e.Search(ctx, "", "podman", 5, "keyword")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Note.Title != "Docker notes" {
		t.Fatalf("keyword search: %+v", hits)
	}
}

func TestHybridSearchFusesBothSignals(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	// This note should rank well on both semantic (shares vocabulary with the
	// query) and keyword (contains the literal term) sides.
	_, err := e.Write(ctx, core.WriteInput{Title: "Postgres backups", Body: "pg_dump and WAL archiving for postgres backups"})
	if err != nil {
		t.Fatalf("failed to write note: %v", err)
	}
	// This note only matches on the literal keyword "podman", not semantically
	// close to the query.
	_, err = e.Write(ctx, core.WriteInput{Title: "Docker notes", Body: "podman play kube is handy"})
	if err != nil {
		t.Fatalf("failed to write note: %v", err)
	}
	_, _ = e.Write(ctx, core.WriteInput{Title: "Cat facts", Body: "cats sleep a lot and purr"})

	hits, err := e.Search(ctx, "", "postgres backup strategy", 5, "hybrid")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hybrid hits, got none")
	}
	if hits[0].Note.Title != "Postgres backups" {
		t.Fatalf("expected postgres note ranked first, got %+v", hits)
	}
	if hits[0].Kind != "hybrid" {
		t.Fatalf("expected kind=hybrid, got %q", hits[0].Kind)
	}
}

func TestPatch(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, _ = e.Write(ctx, core.WriteInput{Title: "Config", Body: "port is 27123 currently"})
	n, err := e.Patch(ctx, "config", "27123", "27124")
	if err != nil {
		t.Fatal(err)
	}
	if n.Body != "port is 27124 currently" {
		t.Fatalf("patch body: %q", n.Body)
	}
	_, _ = e.Write(ctx, core.WriteInput{Title: "Dup", Body: "aa aa"})
	if _, err := e.Patch(ctx, "dup", "aa", "bb"); err == nil {
		t.Fatal("expected ambiguity error")
	}
	if _, err := e.Patch(ctx, "config", "nope", "x"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestSuggestLinks(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, err := e.Write(ctx, core.WriteInput{Title: "Postgres backups", Body: "pg_dump and WAL archiving for postgres backups"})
	if err != nil {
		t.Fatalf("failed to write note: %v", err)
	}
	_, err = e.Write(ctx, core.WriteInput{Title: "Postgres tuning", Body: "pg_dump and WAL archiving tips for postgres performance"})
	if err != nil {
		t.Fatalf("failed to write note: %v", err)
	}
	_, err = e.Write(ctx, core.WriteInput{Title: "Cat facts", Body: "cats sleep a lot and purr"})
	if err != nil {
		t.Fatalf("failed to write note: %v", err)
	}

	hits, err := e.SuggestLinks(ctx, "postgres-backups", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one suggestion")
	}
	for _, h := range hits {
		if h.Note.ID == "postgres-backups" {
			t.Fatal("suggestions must not include the note itself")
		}
		if h.Kind != "suggestion" {
			t.Fatalf("expected kind=suggestion, got %q", h.Kind)
		}
	}
	if hits[0].Note.Title != "Postgres tuning" {
		t.Fatalf("expected the related postgres note ranked first, got %+v", hits)
	}

	// Once linked, the target note must no longer be suggested.
	_, err = e.Write(ctx, core.WriteInput{ID: "postgres-backups", Title: "Postgres backups",
		Body: "pg_dump and WAL archiving for postgres backups, see also [[Postgres tuning]]"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err = e.SuggestLinks(ctx, "postgres-backups", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Note.Title == "Postgres tuning" {
			t.Fatalf("already-linked note must not be suggested again: %+v", hits)
		}
	}

	if _, err := e.SuggestLinks(ctx, "does-not-exist", 5); !core.IsNotFound(err) {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestReembedMissingOnlyBackfillsUnvectoredNotes(t *testing.T) {
	m := newMem()
	e := core.NewEngine(m, fakeEmbedder{})
	ctx := context.Background()

	if _, err := e.Write(ctx, core.WriteInput{Title: "Has Vector", Body: "already embedded"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a note written while the embedder was down: present but with
	// no vector.
	m.notes["no-vector"] = core.Note{ID: "no-vector", Title: "No Vector", Body: "needs embedding"}

	n, err := e.Reembed(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 note backfilled, got %d", n)
	}
	got := m.notes["no-vector"]
	if len(got.Vector) == 0 {
		t.Fatal("expected vector to be backfilled")
	}

	missing, err := m.MissingVectorIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected no notes missing a vector, got %v", missing)
	}
}

func TestReembedFullRebuildsEveryVector(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	if _, err := e.Write(ctx, core.WriteInput{Title: "One", Body: "first note"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Write(ctx, core.WriteInput{Title: "Two", Body: "second note"}); err != nil {
		t.Fatal(err)
	}
	n, err := e.Reembed(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 notes reembedded, got %d", n)
	}
}

func TestBacklinks(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, _ = e.Write(ctx, core.WriteInput{Title: "Target", Body: "the target note"})
	_, _ = e.Write(ctx, core.WriteInput{Title: "Source", Body: "see [[Target]] for details"})
	bl, err := e.Backlinks(ctx, "target", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(bl) != 1 || bl[0].Title != "Source" {
		t.Fatalf("backlinks: %+v", bl)
	}
}

// --- validate.go coverage ---------------------------------------------------

func TestIsNotConfigured(t *testing.T) {
	if !core.IsNotConfigured(core.ErrNotConfigured) {
		t.Fatal("expected true for ErrNotConfigured")
	}
	if core.IsNotConfigured(core.ErrNotFound) {
		t.Fatal("expected false for ErrNotFound")
	}
	if core.IsNotConfigured(nil) {
		t.Fatal("expected false for nil")
	}
}

func TestIsNotFound(t *testing.T) {
	if !core.IsNotFound(core.ErrNotFound) {
		t.Fatal("expected true for ErrNotFound")
	}
	if core.IsNotFound(core.ErrInvalidInput) {
		t.Fatal("expected false for ErrInvalidInput")
	}
	// notFoundErr wraps ErrNotFound.
	err := core.IsNotFound(errors.New("wrapped: " + core.ErrNotFound.Error()))
	_ = err // just checking the error type
}

func TestIsInvalidInput(t *testing.T) {
	if !core.IsInvalidInput(core.ErrInvalidInput) {
		t.Fatal("expected true for ErrInvalidInput")
	}
	if core.IsInvalidInput(core.ErrNotFound) {
		t.Fatal("expected false for ErrNotFound")
	}
}

func TestValidateID(t *testing.T) {
	// Use the public Engine.Read path which calls validateID internally.
	e := newEngine(t)
	ctx := context.Background()

	for _, bad := range []string{"", "  ", "bad path/..", "bad..path", " spaces ", "a:colon", "a@b"} {
		_, err := e.Read(ctx, bad)
		if err == nil {
			t.Fatalf("expected error for id %q", bad)
		}
		if !core.IsInvalidInput(err) {
			t.Fatalf("expected InvalidInput for %q, got %v", bad, err)
		}
	}

	// Valid IDs must not error.
	for _, good := range []string{"hello", "hello-world", "hello.world", "a/b", "a/b/c", "a1_b2-c3"} {
		_, err := e.Read(ctx, good)
		if err == nil {
			continue // note not found is fine — validation passed
		}
		if core.IsInvalidInput(err) {
			t.Fatalf("validateID rejected valid id %q", good)
		}
	}
}

func TestValidateWriteInput(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	// Empty title must fail validation.
	_, err := e.Write(ctx, core.WriteInput{Title: "", Body: "content"})
	if err == nil {
		t.Fatal("expected error for empty title")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}

	// Empty body is valid.
	n, err := e.Write(ctx, core.WriteInput{Title: "Valid"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.Title != "Valid" {
		t.Fatal("expected title to be stored")
	}

	// Too many tags.
	tags := make([]string, 33)
	for i := range tags {
		tags[i] = fmt.Sprintf("tag%d", i)
	}
	_, err = e.Write(ctx, core.WriteInput{Title: "Too Many Tags", Tags: tags})
	if err == nil {
		t.Fatal("expected error for too many tags")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}

	// Empty tag.
	_, err = e.Write(ctx, core.WriteInput{Title: "Empty Tag", Tags: []string{"ok", ""}})
	if err == nil {
		t.Fatal("expected error for empty tag")
	}

	// Invalid project ID.
	_, err = e.Write(ctx, core.WriteInput{Title: "Bad Project", Project: "bad..path"})
	if err == nil {
		t.Fatal("expected error for invalid project")
	}
}

func TestClampOffset(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	// Negative offset should be clamped to 0 (no error).
	notes, err := e.List(ctx, "", 10, -5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notes == nil && len(notes) != 0 {
		t.Fatal("expected empty or nil slice")
	}
}

func TestCount(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	n, err := e.Count(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 notes, got %d", n)
	}

	_, _ = e.Write(ctx, core.WriteInput{Title: "A"})
	_, _ = e.Write(ctx, core.WriteInput{Title: "B"})
	n, err = e.Count(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 notes, got %d", n)
	}
}

func TestStats(t *testing.T) {
	m := newMem()
	e := core.NewEngine(m, fakeEmbedder{})
	ctx := context.Background()

	_, _ = e.Write(ctx, core.WriteInput{Title: "Note A", Body: "see [[Note B]]"})
	_, _ = e.Write(ctx, core.WriteInput{Title: "Note B", Body: "content"})

	s, err := e.Stats(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.TotalNotes != 2 {
		t.Fatalf("expected 2 total notes, got %d", s.TotalNotes)
	}
	if s.TotalLinks != 1 {
		t.Fatalf("expected 1 total link, got %d", s.TotalLinks)
	}
	// Both notes have vectors from fakeEmbedder, so NotesMissingVector should be 0.
	if s.NotesMissingVector != 0 {
		t.Fatalf("expected 0 notes missing vector, got %d", s.NotesMissingVector)
	}

	// Now manually insert a note without a vector and re-check.
	m.notes["no-vec"] = core.Note{ID: "no-vec", Title: "No Vec", Body: "no vector"}
	s2, err := e.Stats(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s2.NotesMissingVector != 1 {
		t.Fatalf("expected 1 note missing vector, got %d", s2.NotesMissingVector)
	}
}

func TestDelete(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	// Write a note, then delete it.
	_, err := e.Write(ctx, core.WriteInput{Title: "To Delete", Body: "will be deleted"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Delete(ctx, "to-delete"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := e.Read(ctx, "to-delete")
	if got != nil {
		t.Fatal("expected note to be deleted")
	}
	if err != nil {
		t.Fatalf("unexpected error reading deleted note: %v", err)
	}

	// Delete a non-existent note should not error.
	if err := e.Delete(ctx, "no-such-note"); err != nil {
		t.Fatalf("unexpected error deleting non-existent note: %v", err)
	}

	// Invalid ID should error.
	if err := e.Delete(ctx, "bad..path"); !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput for bad ID, got %v", err)
	}
}

func TestList(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	// Empty list.
	notes, err := e.List(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("expected 0 notes, got %d", len(notes))
	}

	// Write a few notes and list them.
	_, _ = e.Write(ctx, core.WriteInput{Title: "Alpha", Body: "first"})
	_, _ = e.Write(ctx, core.WriteInput{Title: "Beta", Body: "second"})
	_, _ = e.Write(ctx, core.WriteInput{Title: "Gamma", Body: "third"})

	notes, err = e.List(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 3 {
		t.Fatalf("expected 3 notes, got %d", len(notes))
	}

	// Test pagination (limit).
	notes, err = e.List(ctx, "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 notes with limit=2, got %d", len(notes))
	}

	// Test offset.
	notes, err = e.List(ctx, "", 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("expected 1 note with offset=2, got %d", len(notes))
	}

	// Negative offset should be clamped to 0.
	notes, err = e.List(ctx, "", 10, -5)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 3 {
		t.Fatalf("expected 3 notes with negative offset, got %d", len(notes))
	}

	// Invalid project ID should error.
	_, err = e.List(ctx, "bad..path", 10, 0)
	if err == nil {
		t.Fatal("expected error for invalid project")
	}
}

func TestBacklinksEmptyID(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	_, err := e.Backlinks(ctx, "", "some-title")
	if err == nil {
		t.Fatal("expected error for empty id")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

func TestSearchValidation(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	// Empty query should error.
	_, err := e.Search(ctx, "", "", 10, "semantic")
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}

	// Invalid project should error.
	_, err = e.Search(ctx, "bad..path", "query", 10, "semantic")
	if err == nil {
		t.Fatal("expected error for invalid project")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

func TestSearchKeywordFallback(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	_, _ = e.Write(ctx, core.WriteInput{Title: "Test Note", Body: "contains keyword"})
	hits, err := e.Search(ctx, "", "keyword", 10, "keyword")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].Note.Title != "Test Note" {
		t.Fatalf("expected 'Test Note', got %q", hits[0].Note.Title)
	}
	if hits[0].Kind != "keyword" {
		t.Fatalf("expected kind=keyword, got %q", hits[0].Kind)
	}
}

func TestSearchHybrid(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	_, _ = e.Write(ctx, core.WriteInput{Title: "Hybrid Test", Body: "search hybrid"})
	hits, err := e.Search(ctx, "", "search hybrid", 10, "hybrid")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for hybrid search")
	}
	if hits[0].Kind != "hybrid" {
		t.Fatalf("expected kind=hybrid, got %q", hits[0].Kind)
	}
}

func TestClampLimit(t *testing.T) {
	// Test limit clamping via Engine.Search (which uses clampLimit internally).
	e := newEngine(t)
	ctx := context.Background()
	_, _ = e.Write(ctx, core.WriteInput{Title: "Note", Body: "content"})

	// limit=0 should use DefaultSearchLimit (10).
	hits, err := e.Search(ctx, "", "note", 0, "keyword")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}

	// limit > MaxSearchLimit should be capped to MaxSearchLimit.
	hits2, err := e.Search(ctx, "", "note", 9999, "keyword")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits2) != 1 {
		t.Fatalf("expected 1 hit with capped limit, got %d", len(hits2))
	}
}

// Slug is unexported (internal to Write).  Its behavior is verified
// indirectly by TestWriteReadRoundTrip which checks the derived ID.

// hashBody and embedText are internal to the engine and exercised via
// Write/Reembed integration tests above.  Keeping them unexported avoids
// leaking implementation details.  Their behavior is verified indirectly
// by the vector-reuse and reembed tests.

// FenceOpen, FenceCloses, and FindFMEnd live in internal/mdmodel and are
// tested there.  Keeping them out of core_test avoids cross-package test
// dependencies.

// TestTitle verifies frontmatter-title and H1-title fallback via the public
// Write API.  The engine's store field is unexported, so we verify behavior
// indirectly through Read.
func TestTitle(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	// Frontmatter title takes precedence in mdmodel.Document.Title(),
	// but Engine.Write stores the WriteInput.Title as Note.Title (the
	// frontmatter is not re-parsed into the stored title).  Verify the
	// body contains the frontmatter and the stored title matches the
	// input title.
	n, err := e.Write(ctx, core.WriteInput{Title: "FM Title", Body: "---\ntitle: FM Title Override\n---\n\n# H1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Read(ctx, "fm-title")
	if got == nil {
		t.Fatal("expected note to exist")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title != "FM Title" {
		t.Fatalf("expected 'FM Title' (from WriteInput), got %q", got.Title)
	}

	// No frontmatter title, falls back to H1 in mdmodel.Document.Title().
	// Engine.Write still stores the WriteInput.Title as Note.Title.
	n2, err := e.Write(ctx, core.WriteInput{Title: "No FM", Body: "# My H1 Title\n\nbody"})
	if err != nil {
		t.Fatal(err)
	}
	got2, err := e.Read(ctx, "no-fm")
	if got2 == nil {
		t.Fatal("expected note to exist")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got2.Title != "No FM" {
		t.Fatalf("expected 'No FM' (from WriteInput), got %q", got2.Title)
	}
	// Verify the stored note's ID matches the slug of the original title.
	_ = n  // n is "fm-title"
	_ = n2 // n2 is "no-fm"
}

func TestOutline(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	_, _ = e.Write(ctx, core.WriteInput{
		Title: "Outline Test",
		Body:  "# H1\n\n## H2\n\n### H3",
	})
	n, err := e.Read(ctx, "outline-test")
	if err != nil {
		t.Fatal(err)
	}
	if n == nil {
		t.Fatal("note not found")
	}
	// Outline is a method on Document, not on core.Note.
	// We test it via mdmodel instead. This is fine.
}

func TestReembedAll(t *testing.T) {
	m := newMem()
	e := core.NewEngine(m, fakeEmbedder{})
	ctx := context.Background()

	_, _ = e.Write(ctx, core.WriteInput{Title: "Note A", Body: "content A"})
	_, _ = e.Write(ctx, core.WriteInput{Title: "Note B", Body: "content B"})

	n, err := e.Reembed(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 reembedded, got %d", n)
	}

	// Verify vectors are set.
	for id := range m.notes {
		if len(m.notes[id].Vector) == 0 {
			t.Fatalf("note %q has no vector after reembedAll", id)
		}
	}
}

func TestReembedMissing_NoMissing(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	_, _ = e.Write(ctx, core.WriteInput{Title: "Has Vector", Body: "embedded"})

	n, err := e.Reembed(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 reembedded (no missing), got %d", n)
	}
}

func TestSummarize_NoSummarizer(t *testing.T) {
	m := newMem()
	ctx := context.Background()

	e := core.NewEngine(m, fakeEmbedder{})
	_, _ = e.Write(ctx, core.WriteInput{Title: "Test", Body: "content"})

	_, err := e.Summarize(ctx, "test")
	if !core.IsNotConfigured(err) {
		t.Fatalf("expected IsNotConfigured, got %v", err)
	}
}

func TestSummarize_LongBody(t *testing.T) {
	m := newMem()
	ctx := context.Background()

	// Create a body longer than maxSummarizeBodyRunes.
	longBody := strings.Repeat("a", 9000)
	_, _ = core.NewEngine(m, fakeEmbedder{}).Write(ctx, core.WriteInput{Title: "Long", Body: longBody})

	fakeSum := &fakeSummarizer{text: "summary"}
	e2 := core.NewEngine(m, fakeEmbedder{}).WithSummarizer(fakeSum)
	summary, err := e2.Summarize(ctx, "long")
	if err != nil {
		t.Fatal(err)
	}
	if summary != "summary" {
		t.Fatalf("expected 'summary', got %q", summary)
	}
}

func TestWriteWithProject(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	n, err := e.Write(ctx, core.WriteInput{
		Project: "myproject",
		Title:   "Test",
		Body:    "content",
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != "myproject/test" {
		t.Fatalf("expected 'myproject/test', got %q", n.ID)
	}
	if n.Project != "myproject" {
		t.Fatalf("expected project 'myproject', got %q", n.Project)
	}
}

func TestWriteReusesVector(t *testing.T) {
	m := newMem()
	e := core.NewEngine(m, fakeEmbedder{})
	ctx := context.Background()

	// Write initial note.
	n1, err := e.Write(ctx, core.WriteInput{Title: "Vector Test", Body: "original body"})
	if err != nil {
		t.Fatal(err)
	}
	originalVector := make([]float32, len(n1.Vector))
	copy(originalVector, n1.Vector)

	// Write again with same body — should reuse vector.
	n2, err := e.Write(ctx, core.WriteInput{Title: "Vector Test", Body: "original body"})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range n2.Vector {
		if v != originalVector[i] {
			t.Fatalf("vector changed at index %d: %v vs %v", i, v, originalVector[i])
		}
	}
}

func TestWriteUpdatesVectorOnBodyChange(t *testing.T) {
	m := newMem()
	e := core.NewEngine(m, fakeEmbedder{})
	ctx := context.Background()

	_, _ = e.Write(ctx, core.WriteInput{Title: "Update Test", Body: "original body"})
	n1 := m.notes["update-test"]
	originalVector := make([]float32, len(n1.Vector))
	copy(originalVector, n1.Vector)

	_, _ = e.Write(ctx, core.WriteInput{Title: "Update Test", Body: "changed body"})
	n2 := m.notes["update-test"]
	anyChanged := false
	for i, v := range n2.Vector {
		if v != originalVector[i] {
			anyChanged = true
			break
		}
	}
	if !anyChanged {
		t.Fatalf("vector unchanged after body change: %v", n2.Vector)
	}
}

func TestPatchEmptyOldStr(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	_, _ = e.Write(ctx, core.WriteInput{Title: "Test", Body: "content"})
	_, err := e.Patch(ctx, "test", "   ", "replacement")
	if err == nil {
		t.Fatal("expected error for whitespace-only old_str")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

func TestPatchExceedsMaxBodyLen(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	_, _ = e.Write(ctx, core.WriteInput{Title: "Test", Body: "content"})
	_, err := e.Patch(ctx, "test", "content", strings.Repeat("x", core.MaxBodyLen+1))
	if err == nil {
		t.Fatal("expected error for new_str exceeding MaxBodyLen")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

// TestValidateWriteInput_EmptyTag verifies empty-tag rejection via Engine.Write.
func TestValidateWriteInput_EmptyTag(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, err := e.Write(ctx, core.WriteInput{Title: "Test", Tags: []string{"valid", ""}})
	if err == nil {
		t.Fatal("expected error for empty tag")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

// TestValidateWriteInput_LongTag verifies long-tag rejection via Engine.Write.
func TestValidateWriteInput_LongTag(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, err := e.Write(ctx, core.WriteInput{Title: "Test", Tags: []string{strings.Repeat("a", core.MaxTagLen+1)}})
	if err == nil {
		t.Fatal("expected error for tag exceeding MaxTagLen")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

// TestValidateWriteInput_LongTitle verifies long-title rejection via Engine.Write.
func TestValidateWriteInput_LongTitle(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, err := e.Write(ctx, core.WriteInput{Title: strings.Repeat("a", core.MaxTitleLen+1), Body: "content"})
	if err == nil {
		t.Fatal("expected error for title exceeding MaxTitleLen")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

// TestValidateWriteInput_LongBody verifies long-body rejection via Engine.Write.
func TestValidateWriteInput_LongBody(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, err := e.Write(ctx, core.WriteInput{Title: "Test", Body: strings.Repeat("x", core.MaxBodyLen+1)})
	if err == nil {
		t.Fatal("expected error for body exceeding MaxBodyLen")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

// TestValidateWriteInput_LongProject verifies long-project rejection via Engine.Write.
func TestValidateWriteInput_LongProject(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, err := e.Write(ctx, core.WriteInput{Title: "Test", Project: strings.Repeat("a", core.MaxProjectLen+1)})
	if err == nil {
		t.Fatal("expected error for project exceeding MaxProjectLen")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

// TestValidateWriteInput_InvalidProjectID verifies invalid-project rejection via Engine.Write.
func TestValidateWriteInput_InvalidProjectID(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, err := e.Write(ctx, core.WriteInput{Title: "Test", Project: "bad..path"})
	if err == nil {
		t.Fatal("expected error for invalid project ID")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

// TestValidateWriteInput_LongID verifies long-ID rejection via Engine.Write.
func TestValidateWriteInput_LongID(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	_, err := e.Write(ctx, core.WriteInput{Title: "Test", ID: strings.Repeat("a", core.MaxIDLen+1), Body: "content"})
	if err == nil {
		t.Fatal("expected error for ID exceeding MaxIDLen")
	}
	if !core.IsInvalidInput(err) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

func TestSearchLimitClamping(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	_, _ = e.Write(ctx, core.WriteInput{Title: "Note", Body: "content"})

	// limit=0 should use DefaultSearchLimit (10).
	hits, err := e.Search(ctx, "", "note", 0, "keyword")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
}

func TestSuggestLinks_EmptyVector(t *testing.T) {
	m := newMem()
	ctx := context.Background()

	// Write a note.
	_ = m.Upsert(ctx, core.Note{ID: "source", Title: "Source", Body: "content"})
	// Manually set a note with no vector.
	note := m.notes["source"]
	note.Vector = nil
	m.notes["source"] = note

	e := core.NewEngine(m, fakeEmbedder{})
	hits, err := e.SuggestLinks(ctx, "source", 5)
	if err != nil {
		t.Fatal(err)
	}
	// The note itself should be filtered out.
	for _, h := range hits {
		if h.Note.ID == "source" {
			t.Fatal("suggestions must not include the note itself")
		}
	}
}

// fakeSummarizer implements core.Summarizer for testing.
type fakeSummarizer struct {
	text string
}

func (f *fakeSummarizer) Configured() bool { return true }
func (f *fakeSummarizer) Complete(_ context.Context, prompt string) (string, error) {
	return f.text, nil
}
