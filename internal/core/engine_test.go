package core_test

import (
	"context"
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
func (m *memStore) Get(_ context.Context, id string) (*core.Note, error) {
	n, ok := m.notes[id]
	if !ok {
		return nil, nil
	}
	return &n, nil
}
func (m *memStore) Delete(_ context.Context, id string) error { delete(m.notes, id); return nil }
func (m *memStore) List(_ context.Context, limit, offset int) ([]core.Note, error) {
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
func (m *memStore) Count(_ context.Context) (int, error) { return len(m.notes), nil }
func (m *memStore) MissingVectorIDs(_ context.Context) ([]string, error) {
	var out []string
	for id, n := range m.notes {
		if len(n.Vector) == 0 {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}
func (m *memStore) SearchSemantic(_ context.Context, q []float32, limit int) ([]core.SearchHit, error) {
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
func (m *memStore) KeywordSearch(_ context.Context, q string, limit int) ([]core.Note, error) {
	var out []core.Note
	ql := strings.ToLower(q)
	for _, n := range m.notes {
		if strings.Contains(strings.ToLower(n.Title), ql) || strings.Contains(strings.ToLower(n.Body), ql) {
			out = append(out, n)
		}
	}
	return out, nil
}
func (m *memStore) Backlinks(_ context.Context, idOrTitle string) ([]core.Backlink, error) {
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

// mustWrite seeds a note, failing the test on error.
func mustWrite(t *testing.T, e *core.Engine, ctx context.Context, in core.WriteInput) {
	t.Helper()
	if _, err := e.Write(ctx, in); err != nil {
		t.Fatalf("seed write %q: %v", in.Title, err)
	}
}

func TestSemanticSearchRanks(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	mustWrite(t, e, ctx, core.WriteInput{Title: "Postgres backups", Body: "pg_dump and WAL archiving for postgres backups"})
	mustWrite(t, e, ctx, core.WriteInput{Title: "Cat facts", Body: "cats sleep a lot and purr"})
	hits, err := e.Search(ctx, "postgres backup strategy", 5, "semantic")
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
	mustWrite(t, e, ctx, core.WriteInput{Title: "Docker notes", Body: "podman play kube is handy"})
	hits, err := e.Search(ctx, "podman", 5, "keyword")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Note.Title != "Docker notes" {
		t.Fatalf("keyword search: %+v", hits)
	}
}

func TestPatch(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	mustWrite(t, e, ctx, core.WriteInput{Title: "Config", Body: "port is 27123 currently"})
	n, err := e.Patch(ctx, "config", "27123", "27124")
	if err != nil {
		t.Fatal(err)
	}
	if n.Body != "port is 27124 currently" {
		t.Fatalf("patch body: %q", n.Body)
	}
	mustWrite(t, e, ctx, core.WriteInput{Title: "Dup", Body: "aa aa"})
	if _, err := e.Patch(ctx, "dup", "aa", "bb"); err == nil {
		t.Fatal("expected ambiguity error")
	}
	if _, err := e.Patch(ctx, "config", "nope", "x"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestBacklinks(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	mustWrite(t, e, ctx, core.WriteInput{Title: "Target", Body: "the target note"})
	mustWrite(t, e, ctx, core.WriteInput{Title: "Source", Body: "see [[Target]] for details"})
	bl, err := e.Backlinks(ctx, "target")
	if err != nil {
		t.Fatal(err)
	}
	if len(bl) != 1 || bl[0].Title != "Source" {
		t.Fatalf("backlinks: %+v", bl)
	}
}

// --- degraded-mode behavior --------------------------------------------------

type failEmbedder struct{}

func (failEmbedder) Model() string { return "down" }
func (failEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, fmt.Errorf("connection refused")
}

// countingEmbedder wraps fakeEmbedder and counts calls.
type countingEmbedder struct{ calls int }

func (c *countingEmbedder) Model() string { return "fake" }
func (c *countingEmbedder) Embed(ctx context.Context, s string) ([]float32, error) {
	c.calls++
	return fakeEmbedder{}.Embed(ctx, s)
}

func TestWriteDegradesWhenEmbedderDown(t *testing.T) {
	mem := newMem()
	down := core.NewEngine(mem, failEmbedder{})
	ctx := context.Background()

	// Write must succeed even with the embedder unreachable.
	if _, err := down.Write(ctx, core.WriteInput{Title: "Offline note", Body: "written while the studio was off"}); err != nil {
		t.Fatalf("degraded write should succeed: %v", err)
	}
	if n, err := down.MissingVectors(ctx); err != nil || n != 1 {
		t.Fatalf("missing vectors: n=%d err=%v", n, err)
	}

	// Semantic search degrades to keyword and still finds it.
	hits, err := down.Search(ctx, "studio", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Kind != "keyword" {
		t.Fatalf("expected keyword-fallback hit, got %+v", hits)
	}

	// Embedder comes back: backfill only the missing note.
	up := core.NewEngine(mem, fakeEmbedder{})
	n, err := up.Reembed(ctx, true)
	if err != nil || n != 1 {
		t.Fatalf("reembed missing: n=%d err=%v", n, err)
	}
	if n, _ := up.MissingVectors(ctx); n != 0 {
		t.Fatalf("still missing %d after backfill", n)
	}
	hits, err = up.Search(ctx, "note written while offline", 5, "semantic")
	if err != nil || len(hits) == 0 {
		t.Fatalf("semantic search after backfill: hits=%v err=%v", hits, err)
	}
}

func TestWriteReusesVectorOnIdenticalBody(t *testing.T) {
	ce := &countingEmbedder{}
	e := core.NewEngine(newMem(), ce)
	ctx := context.Background()

	in := core.WriteInput{Title: "Stable", Body: "same body both times"}
	mustWrite(t, e, ctx, in)
	if ce.calls != 1 {
		t.Fatalf("first write: %d embed calls", ce.calls)
	}
	// Metadata-only rewrite (same body) must not re-embed.
	in.Tags = []string{"retagged"}
	mustWrite(t, e, ctx, in)
	if ce.calls != 1 {
		t.Fatalf("identical-body rewrite re-embedded: %d calls", ce.calls)
	}
	// Changed body must re-embed.
	in.Body = "different body now"
	mustWrite(t, e, ctx, in)
	if ce.calls != 2 {
		t.Fatalf("changed-body rewrite: %d calls", ce.calls)
	}
}
