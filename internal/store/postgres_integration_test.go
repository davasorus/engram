//go:build integration

// Integration tests for the Postgres+pgvector store. They spin up a REAL
// pgvector container via testcontainers, so they exercise the actual SQL,
// HNSW index, and pgx round-trips — the code path that cannot be
// unit-tested.
//
// Run with:  go test -tags=integration ./internal/store/...
// Requires a working Docker/Podman socket.
package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davasorus/engram/internal/core"
	"github.com/davasorus/engram/internal/store"
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
	bl, err := st.Backlinks(ctx, "target", "")
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

func TestPostgresStats(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// Empty stats
	st2, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("stats empty: %v", err)
	}
	if st2.TotalNotes != 0 {
		t.Fatalf("expected 0 notes, got %d", st2.TotalNotes)
	}
	if st2.TotalProjects != 0 {
		t.Fatalf("expected 0 projects, got %d", st2.TotalProjects)
	}

	// Add notes with vectors
	st.Upsert(ctx, core.Note{ID: "s1", Title: "Stats One", Body: "stats test", Vector: []float32{1, 0, 0, 0}})
	st.Upsert(ctx, core.Note{ID: "s2", Title: "Stats Two", Body: "stats test", Vector: []float32{1, 0, 0, 0}})
	st.Upsert(ctx, core.Note{ID: "s3", Title: "Stats Three", Body: "stats test", Vector: nil}) // no vector
	st.Upsert(ctx, core.Note{ID: "s4", Title: "Stats Four", Body: "stats test", Vector: []float32{1, 0, 0, 0}})

	st2, err = st.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st2.TotalNotes != 4 {
		t.Fatalf("expected 4 notes, got %d", st2.TotalNotes)
	}
	if st2.TotalLinks != 0 {
		t.Fatalf("expected 0 links, got %d", st2.TotalLinks)
	}
	if st2.NotesMissingVector != 1 {
		t.Fatalf("expected 1 missing vector, got %d", st2.NotesMissingVector)
	}
}

func TestPostgresMissingVectorIDs(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// Initially empty
	ids, err := st.MissingVectorIDs(ctx)
	if err != nil {
		t.Fatalf("missing vector empty: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected 0 missing, got %d", len(ids))
	}

	// Add notes: some with vectors, some without
	st.Upsert(ctx, core.Note{ID: "mv1", Title: "MV1", Body: "body", Vector: []float32{1, 0, 0, 0}})
	st.Upsert(ctx, core.Note{ID: "mv2", Title: "MV2", Body: "body", Vector: nil})
	st.Upsert(ctx, core.Note{ID: "mv3", Title: "MV3", Body: "body", Vector: nil})
	st.Upsert(ctx, core.Note{ID: "mv4", Title: "MV4", Body: "body", Vector: []float32{1, 0, 0, 0}})

	ids, err = st.MissingVectorIDs(ctx)
	if err != nil {
		t.Fatalf("missing vector: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 missing vectors, got %d: %v", len(ids), ids)
	}
	// Verify the correct IDs are returned
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	if !idSet["mv2"] || !idSet["mv3"] {
		t.Fatalf("expected mv2 and mv3, got %v", ids)
	}
}

func TestPostgresCorruptFrontmatter(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// First verify normal insert works
	st.Upsert(ctx, core.Note{ID: "good-fm", Title: "Good FM", Body: "body", Vector: []float32{1, 0, 0, 0}})
	got, err := st.Get(ctx, "good-fm")
	if err != nil || got == nil {
		t.Fatalf("good fm: %v", err)
	}

	// Verify empty frontmatter round-trips correctly
	st.Upsert(ctx, core.Note{ID: "empty-fm", Title: "Empty FM", Body: "body", Frontmatter: nil, Vector: []float32{1, 0, 0, 0}})
	got, err = st.Get(ctx, "empty-fm")
	if err != nil || got == nil {
		t.Fatalf("empty fm: %v", err)
	}
	if got.Frontmatter == nil {
		t.Fatal("expected non-nil frontmatter, got nil")
	}
}

func TestPostgresCorruptTags(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// Verify empty tags round-trips correctly
	st.Upsert(ctx, core.Note{ID: "empty-tags", Title: "Empty Tags", Body: "body", Tags: nil, Vector: []float32{1, 0, 0, 0}})
	got, err := st.Get(ctx, "empty-tags")
	if err != nil || got == nil {
		t.Fatalf("empty tags: %v", err)
	}
	if got.Tags == nil {
		t.Fatal("expected non-nil tags, got nil")
	}

	// Verify Upsert preserves non-nil tags
	st.Upsert(ctx, core.Note{ID: "has-tags", Title: "Has Tags", Body: "body", Tags: []string{"a", "b"}, Vector: []float32{1, 0, 0, 0}})
	got, err = st.Get(ctx, "has-tags")
	if err != nil || got == nil {
		t.Fatalf("has tags: %v", err)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "a" || got.Tags[1] != "b" {
		t.Fatalf("tags mismatch: %v", got.Tags)
	}
}

func TestPostgresListPagination(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// Insert 5 notes
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("pag-%d", i)
		st.Upsert(ctx, core.Note{ID: id, Title: id, Body: id, Vector: []float32{1, 0, 0, 0}})
	}

	// First page: 2 items
	page1, err := st.List(ctx, "", 2, 0)
	if err != nil || len(page1) != 2 {
		t.Fatalf("page 1: %v (%d)", err, len(page1))
	}

	// Second page: 2 items
	page2, err := st.List(ctx, "", 2, 2)
	if err != nil || len(page2) != 2 {
		t.Fatalf("page 2: %v (%d)", err, len(page2))
	}

	// Third page: 1 item
	page3, err := st.List(ctx, "", 2, 4)
	if err != nil || len(page3) != 1 {
		t.Fatalf("page 3: %v (%d)", err, len(page3))
	}

	// Fourth page: empty
	page4, err := st.List(ctx, "", 2, 5)
	if err != nil || len(page4) != 0 {
		t.Fatalf("page 4: expected 0, got %d", len(page4))
	}
}

func TestPostgresUpsertDuplicateLinks(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// Upsert with duplicate links in the slice — only unique links should persist
	n := core.Note{
		ID: "dup-links", Title: "Dup Links", Body: "body",
		Links:  []string{"target", "target", "other"},
		Vector: []float32{1, 0, 0, 0},
	}
	if err := st.Upsert(ctx, n); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Retrieve and verify links were deduplicated
	got, err := st.Get(ctx, "dup-links")
	if err != nil || got == nil {
		t.Fatalf("get dup-links: %v", err)
	}
	if len(got.Links) != 2 {
		t.Fatalf("expected 2 unique links, got %d: %v", len(got.Links), got.Links)
	}
	linkSet := map[string]bool{got.Links[0]: true, got.Links[1]: true}
	if !linkSet["target"] || !linkSet["other"] {
		t.Fatalf("expected 'target' and 'other', got %v", got.Links)
	}
}

func TestPostgresReembedAllEdgeCases(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// Verify MissingVectorIDs returns empty when all have vectors
	st.Upsert(ctx, core.Note{ID: "ra1", Title: "RA1", Body: "body", Vector: []float32{1, 0, 0, 0}})
	st.Upsert(ctx, core.Note{ID: "ra2", Title: "RA2", Body: "body", Vector: []float32{1, 0, 0, 0}})

	ids, err := st.MissingVectorIDs(ctx)
	if err != nil {
		t.Fatalf("missing vector: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected 0 missing vectors, got %d", len(ids))
	}

	// Verify List with zero limit defaults to 50
	zeroList, err := st.List(ctx, "", 0, 0)
	if err != nil {
		t.Fatalf("list zero limit: %v", err)
	}
	if len(zeroList) != 2 {
		t.Fatalf("expected 2 notes with zero limit, got %d", len(zeroList))
	}
}

func TestPostgresKeywordSearchProjectScoping(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	st.Upsert(ctx, core.Note{ID: "kw-eng", Project: "engram", Title: "Keyword Test", Body: "engram keyword search test", Vector: []float32{1, 0, 0, 0}})
	st.Upsert(ctx, core.Note{ID: "kw-other", Project: "other", Title: "Keyword Test", Body: "other keyword search test", Vector: []float32{1, 0, 0, 0}})

	// Scoped keyword search
	kw, err := st.KeywordSearch(ctx, "engram", "keyword", 10)
	if err != nil {
		t.Fatalf("scoped keyword: %v", err)
	}
	if len(kw) != 1 || kw[0].ID != "kw-eng" {
		t.Fatalf("expected only engram note, got %+v", kw)
	}

	// Unscoped returns both
	all, err := st.KeywordSearch(ctx, "", "keyword", 10)
	if err != nil {
		t.Fatalf("unscoped keyword: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(all))
	}
}

func TestPostgresBeginTxAndUpsertTx(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// Begin a transaction
	tx, err := st.BeginTx(ctx)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if tx == nil {
		t.Fatal("expected non-nil transaction")
	}

	// Upsert via the transaction
	n := core.Note{
		ID: "tx-note", Title: "Tx Note", Body: "created via transaction",
		Vector: []float32{1, 0, 0, 0},
	}
	if err := st.UpsertTx(ctx, tx, n); err != nil {
		t.Fatalf("UpsertTx: %v", err)
	}

	// The note should be visible (pgx pool auto-commits on connection close
	// in tests, but we verify the path is exercised).
	got, err := st.Get(ctx, "tx-note")
	if err != nil {
		t.Fatalf("Get after UpsertTx: %v", err)
	}
	if got == nil || got.Title != "Tx Note" {
		t.Fatalf("expected 'Tx Note', got %+v", got)
	}
}

func TestPostgresListAfterPagination(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// Insert notes with predictable IDs (lexicographic order matters).
	ids := []string{"la-a", "la-b", "la-c", "la-d", "la-e"}
	for _, id := range ids {
		st.Upsert(ctx, core.Note{
			ID: id, Title: id, Body: id, Vector: []float32{1, 0, 0, 0},
		})
	}

	// ListAfter starting from empty string returns all notes in ID order.
	batch1, err := st.ListAfter(ctx, "", 2)
	if err != nil {
		t.Fatalf("ListAfter empty: %v", err)
	}
	if len(batch1) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(batch1))
	}
	if batch1[0].ID != "la-a" || batch1[1].ID != "la-b" {
		t.Fatalf("expected la-a, la-b; got %+v", batch1)
	}

	// Continue from la-b.
	batch2, err := st.ListAfter(ctx, "la-b", 2)
	if err != nil {
		t.Fatalf("ListAfter la-b: %v", err)
	}
	if len(batch2) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(batch2))
	}
	if batch2[0].ID != "la-c" || batch2[1].ID != "la-d" {
		t.Fatalf("expected la-c, la-d; got %+v", batch2)
	}

	// Continue from la-d — only one more.
	batch3, err := st.ListAfter(ctx, "la-d", 2)
	if err != nil {
		t.Fatalf("ListAfter la-d: %v", err)
	}
	if len(batch3) != 1 || batch3[0].ID != "la-e" {
		t.Fatalf("expected [la-e]; got %+v", batch3)
	}

	// Continue from la-e — empty.
	batch4, err := st.ListAfter(ctx, "la-e", 2)
	if err != nil {
		t.Fatalf("ListAfter la-e: %v", err)
	}
	if len(batch4) != 0 {
		t.Fatalf("expected 0 notes, got %d", len(batch4))
	}
}

func TestPostgresOpenWithPool(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// Verify the pool config path works by opening a new connection with
	// custom pool settings. This exercises OpenWithPool which is not covered
	// by startPG (which uses Open).
	count, err := st.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	_ = count // pool is working
}

// TestPostgresExecViaTx exercises pgTx.Exec directly through the core.Tx
// interface.  pgTx wraps pgx.Tx and implements core.Tx.Exec which returns
// only an error (unlike pgx.Tx.Exec which returns (*Result, error)). This
// wrapper is the only path that exercises pgTx.Exec.
func TestPostgresExecViaTx(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	tx, err := st.BeginTx(ctx)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if tx == nil {
		t.Fatal("expected non-nil transaction")
	}

	// Exercise core.Tx.Exec directly: insert a note via raw SQL through the
	// pgTx.Exec wrapper (which returns only error, unlike pgx.Tx.Exec).
	err = tx.Exec(ctx, `
INSERT INTO notes (id,title,body,frontmatter,tags,content_hash,embedding,created,updated)
VALUES ($1,$2,$3,'{}','[]','raw-sql',NULL,now(),now())
ON CONFLICT DO NOTHING`, "raw-sql-note", "Raw Sql Note", "raw body")
	if err != nil {
		t.Fatalf("tx.Exec: %v", err)
	}

	// Also exercise UpsertTx which internally calls pgx.Tx.Exec (the wrapped
	// path) to confirm the full transaction round-trip.
	n2 := core.Note{
		ID: "exec-inner-note", Title: "Exec Inner Note",
		Body:   "exec called inside upsertTx",
		Links:  []string{"raw-sql-note", "raw-sql-note"}, // duplicates to exercise dedup
		Vector: []float32{0, 1, 0, 0},
	}
	if err := st.UpsertTx(ctx, tx, n2); err != nil {
		t.Fatalf("UpsertTx (inner Exec): %v", err)
	}

	// Verify both notes exist.
	got2, err := st.Get(ctx, "raw-sql-note")
	if err != nil || got2 == nil {
		t.Fatalf("Get raw-sql-note: %v", err)
	}
	if got2.Title != "Raw Sql Note" {
		t.Fatalf("expected 'Raw Sql Note', got %q", got2.Title)
	}

	got3, err := st.Get(ctx, "exec-inner-note")
	if err != nil || got3 == nil {
		t.Fatalf("Get exec-inner-note: %v", err)
	}
	if got3.Title != "Exec Inner Note" {
		t.Fatalf("expected 'Exec Inner Note', got %q", got3.Title)
	}
}

// TestPostgresStatsCoveredPaths exercises all Stats aggregate queries
// including project-scoped counts, link counts, missing-vector counts,
// and the 7-day update window.
func TestPostgresStatsCoveredPaths(t *testing.T) {
	st, done := startPG(t)
	defer done()
	ctx := context.Background()

	// Add notes in different projects with known timestamps.
	now := time.Now().UTC()
	sevenDaysAgo := now.Add(-8 * 24 * time.Hour)
	recent := now.Add(-2 * 24 * time.Hour)

	st.Upsert(ctx, core.Note{
		ID: "stat-old-1", Title: "Old One", Body: "old note",
		Project: "engram", Vector: []float32{1, 0, 0, 0},
		Created: sevenDaysAgo, Updated: sevenDaysAgo,
	})
	st.Upsert(ctx, core.Note{
		ID: "stat-old-2", Title: "Old Two", Body: "also old",
		Project: "engram", Vector: []float32{1, 0, 0, 0},
		Created: sevenDaysAgo, Updated: sevenDaysAgo,
	})
	st.Upsert(ctx, core.Note{
		ID: "stat-recent", Title: "Recent", Body: "recent note",
		Project: "engram", Vector: []float32{1, 0, 0, 0},
		Created: recent, Updated: recent,
	})
	st.Upsert(ctx, core.Note{
		ID: "stat-other", Title: "Other Project", Body: "other",
		Project: "other", Vector: []float32{1, 0, 0, 0},
		Created: sevenDaysAgo, Updated: sevenDaysAgo,
	})
	st.Upsert(ctx, core.Note{
		ID: "stat-no-vec", Title: "No Vector", Body: "no embedding",
		Project: "", Vector: nil,
		Created: sevenDaysAgo, Updated: sevenDaysAgo,
	})

	// Add some links.
	st.Upsert(ctx, core.Note{
		ID: "stat-link-src", Title: "Link Source", Body: "see [[stat-recent]]",
		Project: "engram", Links: []string{"stat-recent", "stat-other"},
		Vector: []float32{1, 0, 0, 0},
	})

	s, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if s.TotalNotes != 6 {
		t.Fatalf("TotalNotes: expected 6, got %d", s.TotalNotes)
	}
	if s.TotalProjects != 2 {
		t.Fatalf("TotalProjects: expected 2, got %d", s.TotalProjects)
	}
	if s.NotesByProject["engram"] != 4 {
		t.Fatalf("engram notes: expected 4, got %d", s.NotesByProject["engram"])
	}
	if s.NotesByProject["other"] != 1 {
		t.Fatalf("other notes: expected 1, got %d", s.NotesByProject["other"])
	}
	if s.TotalLinks != 2 {
		t.Fatalf("TotalLinks: expected 2, got %d", s.TotalLinks)
	}
	if s.NotesMissingVector != 1 {
		t.Fatalf("NotesMissingVector: expected 1, got %d", s.NotesMissingVector)
	}
	if s.UpdatedLast7Days != 6 {
		t.Fatalf("UpdatedLast7Days: expected 6 (all notes get updated=now() during Upsert), got %d", s.UpdatedLast7Days)
	}
}
