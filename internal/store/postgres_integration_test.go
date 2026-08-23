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
	"fmt"
	"testing"
	"time"

	"github.com/davasorus/engram/internal/core"
	"github.com/davasorus/engram/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// applySchema creates the schema the store expects. Production schema is owned
// by Liquibase (db/changelog); this mirrors changeset 001 so the store's query
// paths can be exercised in isolation without running Liquibase in the test.
// Kept deliberately in lockstep with db/changelog/changes/001-initial-schema.sql.
func applySchema(t *testing.T, ctx context.Context, dsn string, dims int) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("schema connect: %v", err)
	}
	defer conn.Close(ctx)
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS notes (
  id TEXT PRIMARY KEY, title TEXT NOT NULL,
  body TEXT NOT NULL, frontmatter JSONB NOT NULL DEFAULT '{}', tags JSONB NOT NULL DEFAULT '[]',
  content_hash TEXT NOT NULL DEFAULT '', embedding vector(%d),
  created TIMESTAMPTZ NOT NULL DEFAULT now(), updated TIMESTAMPTZ NOT NULL DEFAULT now())`, dims),
		// project is an additive migration (db/changelog 002) — nullable column.
		`ALTER TABLE notes ADD COLUMN IF NOT EXISTS project TEXT`,
		`CREATE TABLE IF NOT EXISTS links (src TEXT NOT NULL REFERENCES notes(id) ON DELETE CASCADE, dst TEXT NOT NULL, PRIMARY KEY (src, dst))`,
		`CREATE INDEX IF NOT EXISTS idx_links_dst ON links(dst)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_updated ON notes(updated DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_project ON notes(project)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_embedding ON notes USING hnsw (embedding vector_cosine_ops)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_fts ON notes USING gin (to_tsvector('english', title || ' ' || body))`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("apply schema: %v", err)
		}
	}
}

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
	applySchema(t, ctx, dsn, 4)        // schema is Liquibase's job in prod; apply directly here
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

	hits, err := st.SearchSemantic(ctx, "", []float32{0.9, 0.1, 0, 0}, 2)
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
	kw, err := st.KeywordSearch(ctx, "", "postgres", 5)
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
	list, err := st.List(ctx, "", 2, 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v (%d)", err, len(list))
	}
}

func TestPostgresProjectScoping(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// two notes in different projects, similar vectors
	must := func(n core.Note) {
		if err := st.Upsert(ctx, n); err != nil {
			t.Fatalf("upsert %s: %v", n.ID, err)
		}
	}
	must(core.Note{ID: "engram/conv", Project: "engram", Title: "Conventions", Body: "engram code conventions", Vector: []float32{1, 0, 0, 0}})
	must(core.Note{ID: "other/conv", Project: "other", Title: "Conventions", Body: "other project conventions", Vector: []float32{1, 0, 0, 0}})

	// scoped semantic search returns only the engram note
	hits, err := st.SearchSemantic(ctx, "engram", []float32{1, 0, 0, 0}, 10)
	if err != nil {
		t.Fatalf("scoped semantic: %v", err)
	}
	if len(hits) != 1 || hits[0].Note.ID != "engram/conv" {
		t.Fatalf("expected only engram note, got %+v", hits)
	}
	if hits[0].Note.Project != "engram" {
		t.Fatalf("project not populated: %q", hits[0].Note.Project)
	}

	// unscoped returns both
	all, _ := st.SearchSemantic(ctx, "", []float32{1, 0, 0, 0}, 10)
	if len(all) != 2 {
		t.Fatalf("unscoped should return 2, got %d", len(all))
	}

	// scoped list
	ln, err := st.List(ctx, "other", 10, 0)
	if err != nil || len(ln) != 1 || ln[0].ID != "other/conv" {
		t.Fatalf("scoped list: %v %+v", err, ln)
	}

	// scoped keyword
	kw, _ := st.KeywordSearch(ctx, "engram", "conventions", 10)
	if len(kw) != 1 || kw[0].ID != "engram/conv" {
		t.Fatalf("scoped keyword: %+v", kw)
	}
}
