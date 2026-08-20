//go:build integration

// Integration tests for the Postgres+pgvector store. They spin up a REAL
// pgvector container via testcontainers, so they exercise the actual SQL,
// HNSW index, and pgx round-trips — the code path that can't be unit-tested.
//
// Run with:  go test -tags=integration ./internal/store/...
// Requires a working Docker/Podman socket.
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/davasorus/engram/internal/core"
	"github.com/davasorus/engram/internal/store"
)

// startPG launches a pgvector container and returns a connected store.
func startPG(t *testing.T) (*store.Postgres, func()) {
	t.Helper()
	ctx := context.Background()
	ctr, err := postgres.Run(ctx,
		"pgvector/pgvector:pg16",
		postgres.WithDatabase("engram"),
		postgres.WithUsername("engram"),
		postgres.WithPassword("engram"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start pgvector container: %v", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	st, err := store.Open(ctx, dsn, 4) // 4-dim vectors keep the test light
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cleanup := func() {
		st.Close()
		_ = ctr.Terminate(ctx)
	}
	return st, cleanup
}

func TestPostgresUpsertGetDelete(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	n := core.Note{
		ID: "sql-ag", Title: "SQL AG", Body: "configure an [[availability group]]",
		Tags: []string{"sql"}, Links: []string{"availability group"},
		Vector: []float32{1, 0, 0, 0}, ContentHash: "h1",
	}
	if err := st.Upsert(ctx, n); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := st.Get(ctx, "sql-ag")
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "SQL AG" || len(got.Links) != 1 || got.Links[0] != "availability group" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "sql" {
		t.Fatalf("tags: %+v", got.Tags)
	}
	if err := st.Delete(ctx, "sql-ag"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := st.Get(ctx, "sql-ag"); got != nil {
		t.Fatal("note not deleted")
	}
}

func TestPostgresSemanticSearch(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// Three orthogonal-ish vectors; query closest to the first.
	must := func(n core.Note) {
		if err := st.Upsert(ctx, n); err != nil {
			t.Fatalf("upsert %s: %v", n.ID, err)
		}
	}
	must(core.Note{ID: "a", Title: "Alpha", Body: "alpha", Vector: []float32{1, 0, 0, 0}})
	must(core.Note{ID: "b", Title: "Beta", Body: "beta", Vector: []float32{0, 1, 0, 0}})
	must(core.Note{ID: "c", Title: "Gamma", Body: "gamma", Vector: []float32{0, 0, 1, 0}})

	hits, err := st.SearchSemantic(ctx, []float32{0.9, 0.1, 0, 0}, 2)
	if err != nil {
		t.Fatalf("semantic search: %v", err)
	}
	if len(hits) == 0 || hits[0].Note.ID != "a" {
		t.Fatalf("expected 'a' first, got %+v", hits)
	}
	if hits[0].Kind != "semantic" {
		t.Fatalf("kind: %s", hits[0].Kind)
	}
	// pgvector cosine: score should be high (close to 1) for the near match.
	if hits[0].Score < 0.9 {
		t.Fatalf("expected high similarity, got %.3f", hits[0].Score)
	}
}

func TestPostgresKeywordAndBacklinks(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	st.Upsert(ctx, core.Note{ID: "target", Title: "Target Note", Body: "postgres tuning notes", Vector: []float32{1, 0, 0, 0}})
	st.Upsert(ctx, core.Note{ID: "source", Title: "Source", Body: "see [[Target Note]]", Links: []string{"target note"}, Vector: []float32{0, 1, 0, 0}})

	// keyword FTS
	kw, err := st.KeywordSearch(ctx, "postgres", 5)
	if err != nil {
		t.Fatalf("keyword: %v", err)
	}
	if len(kw) != 1 || kw[0].ID != "target" {
		t.Fatalf("keyword search: %+v", kw)
	}

	// backlinks: 'source' links to 'target note' (by title)
	bl, err := st.Backlinks(ctx, "target")
	if err != nil {
		t.Fatalf("backlinks: %v", err)
	}
	if len(bl) != 1 || bl[0].ID != "source" {
		t.Fatalf("backlinks: %+v", bl)
	}
}

func TestPostgresCountAndList(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()
	for _, id := range []string{"n1", "n2", "n3"} {
		st.Upsert(ctx, core.Note{ID: id, Title: id, Body: id, Vector: []float32{1, 0, 0, 0}})
	}
	if c, _ := st.Count(ctx); c != 3 {
		t.Fatalf("count: %d", c)
	}
	list, err := st.List(ctx, 2, 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v (%d)", err, len(list))
	}
}

// TestPostgresNullVectorRoundTrip covers degraded writes: a note stored with
// no vector must scan back cleanly (NULL embedding -> empty Vector), show up
// in MissingVectorIDs, and disappear from it once a vector is upserted.
func TestPostgresNullVectorRoundTrip(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	n := core.Note{ID: "degraded", Title: "Degraded", Body: "written while embedder was down", ContentHash: "h1"}
	if err := st.Upsert(ctx, n); err != nil {
		t.Fatalf("upsert without vector: %v", err)
	}

	got, err := st.Get(ctx, "degraded")
	if err != nil {
		t.Fatalf("get with NULL embedding: %v", err)
	}
	if got == nil || len(got.Vector) != 0 {
		t.Fatalf("expected empty vector, got %+v", got)
	}

	ids, err := st.MissingVectorIDs(ctx)
	if err != nil {
		t.Fatalf("missing ids: %v", err)
	}
	if len(ids) != 1 || ids[0] != "degraded" {
		t.Fatalf("missing ids: %v", ids)
	}

	// Backfill: same note with a vector; must scan back and leave the list.
	n.Vector = []float32{0.1, 0.2, 0.3, 0.4}
	if err := st.Upsert(ctx, n); err != nil {
		t.Fatalf("backfill upsert: %v", err)
	}
	got, err = st.Get(ctx, "degraded")
	if err != nil || len(got.Vector) != 4 {
		t.Fatalf("vector after backfill: %+v err=%v", got, err)
	}
	if ids, _ := st.MissingVectorIDs(ctx); len(ids) != 0 {
		t.Fatalf("still listed as missing: %v", ids)
	}
}
