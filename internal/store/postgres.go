// Package store implements core.Store on Postgres + pgvector. Postgres runs as
// its own container in the compose/kube stack; engram connects over a normal
// connection string. pgvector provides real indexed KNN (HNSW) and cosine
// distance in SQL, so similarity search scales well beyond brute force.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/davasorus/engram/internal/core"
)

type Postgres struct {
	pool *pgxpool.Pool
	dims int
}

// Open connects to Postgres, ensures the pgvector extension and schema exist,
// and returns a Store. dims is the embedding dimensionality (e.g. 768 for
// nomic-embed-text). The vector column is created at this width; changing
// models with a different width requires a migration (see docs).
func Open(ctx context.Context, dsn string, dims int) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	p := &Postgres{pool: pool, dims: dims}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return p, nil
}

func (p *Postgres) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS notes (
  id            TEXT PRIMARY KEY,
  project       TEXT NOT NULL DEFAULT '',
  title         TEXT NOT NULL,
  body          TEXT NOT NULL,
  frontmatter   JSONB NOT NULL DEFAULT '{}',
  tags          JSONB NOT NULL DEFAULT '[]',
  content_hash  TEXT NOT NULL DEFAULT '',
  embedding     vector(%d),
  created       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated       TIMESTAMPTZ NOT NULL DEFAULT now()
)`, p.dims),
		// Add project column if upgrading an older table that predates it.
		`ALTER TABLE notes ADD COLUMN IF NOT EXISTS project TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS links (
  src TEXT NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
  dst TEXT NOT NULL,
  PRIMARY KEY (src, dst)
)`,
		`CREATE INDEX IF NOT EXISTS idx_links_dst ON links(dst)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_updated ON notes(updated DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_project ON notes(project)`,
		// HNSW index for cosine distance. Built lazily; fine to exist before rows.
		`CREATE INDEX IF NOT EXISTS idx_notes_embedding ON notes USING hnsw (embedding vector_cosine_ops)`,
		// Full-text search index for keyword fallback.
		`CREATE INDEX IF NOT EXISTS idx_notes_fts ON notes USING gin (to_tsvector('english', title || ' ' || body))`,
	}
	for _, s := range stmts {
		if _, err := p.pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", firstLine(s), err)
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func (p *Postgres) Close() error { p.pool.Close(); return nil }

// --- Store implementation ---------------------------------------------------

func (p *Postgres) Upsert(ctx context.Context, n core.Note) error {
	if n.Created.IsZero() {
		n.Created = time.Now().UTC()
	}
	n.Updated = time.Now().UTC()
	fm, _ := json.Marshal(orEmptyMap(n.Frontmatter))
	tags, _ := json.Marshal(orEmptySlice(n.Tags))

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			// log or handle error if needed, but for now just suppress it as rollback is a best-effort on failure
			_ = fmt.Errorf("rollback failed: %w", err)
		}
	}()

	var emb any
	if len(n.Vector) > 0 {
		emb = pgvector.NewVector(n.Vector)
	}
	_, err = tx.Exec(ctx, `
INSERT INTO notes (id,project,title,body,frontmatter,tags,content_hash,embedding,created,updated)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (id) DO UPDATE SET
  project=EXCLUDED.project, title=EXCLUDED.title, body=EXCLUDED.body, frontmatter=EXCLUDED.frontmatter,
  tags=EXCLUDED.tags, content_hash=EXCLUDED.content_hash, embedding=EXCLUDED.embedding,
  updated=EXCLUDED.updated`,
		n.ID, n.Project, n.Title, n.Body, string(fm), string(tags), n.ContentHash, emb,
		n.Created, n.Updated)
	if err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `DELETE FROM links WHERE src=$1`, n.ID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, l := range n.Links {
		dst := strings.ToLower(strings.TrimSpace(l))
		if dst == "" || seen[dst] {
			continue
		}
		seen[dst] = true
		if _, err = tx.Exec(ctx, `INSERT INTO links(src,dst) VALUES($1,$2) ON CONFLICT DO NOTHING`, n.ID, dst); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (p *Postgres) Get(ctx context.Context, id string) (*core.Note, error) {
	n, err := p.scanOne(ctx, `SELECT id,project,title,body,frontmatter,tags,content_hash,created,updated FROM notes WHERE id=$1`, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	n.Links, _ = p.outgoingLinks(ctx, id)
	return n, nil
}

func (p *Postgres) scanOne(ctx context.Context, q string, args ...any) (*core.Note, error) {
	row := p.pool.QueryRow(ctx, q, args...)
	var (
		n        core.Note
		fm, tags []byte
	)
	if err := row.Scan(&n.ID, &n.Project, &n.Title, &n.Body, &fm, &tags, &n.ContentHash, &n.Created, &n.Updated); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(fm, &n.Frontmatter)
	_ = json.Unmarshal(tags, &n.Tags)
	return &n, nil
}

func (p *Postgres) outgoingLinks(ctx context.Context, id string) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT dst FROM links WHERE src=$1 ORDER BY dst`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (p *Postgres) Delete(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM notes WHERE id=$1`, id)
	return err
}

func (p *Postgres) List(ctx context.Context, project string, limit, offset int) ([]core.Note, error) {
	if limit <= 0 {
		limit = 50
	}
	var err error
	var rows pgx.Rows
	if project != "" {
		rows, err = p.pool.Query(ctx, `SELECT id,project,title,body,frontmatter,tags,content_hash,created,updated FROM notes WHERE project=$1 ORDER BY updated DESC LIMIT $2 OFFSET $3`, project, limit, offset)
	} else {
		rows, err = p.pool.Query(ctx, `SELECT id,project,title,body,frontmatter,tags,content_hash,created,updated FROM notes ORDER BY updated DESC LIMIT $1 OFFSET $2`, limit, offset)
	}
	if err != nil {
		return nil, err
	}
	return p.scanMany(rows)
}

func (p *Postgres) scanMany(rows pgx.Rows) ([]core.Note, error) {
	defer rows.Close()
	var out []core.Note
	for rows.Next() {
		var (
			n        core.Note
			fm, tags []byte
		)
		if err := rows.Scan(&n.ID, &n.Project, &n.Title, &n.Body, &fm, &tags, &n.ContentHash, &n.Created, &n.Updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(fm, &n.Frontmatter)
		_ = json.Unmarshal(tags, &n.Tags)
		out = append(out, n)
	}
	return out, rows.Err()
}

func (p *Postgres) Count(ctx context.Context) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM notes`).Scan(&n)
	return n, err
}

// SearchSemantic runs pgvector KNN directly in SQL — the DB does the ranking,
// so this scales with the HNSW index instead of pulling all vectors into Go.
func (p *Postgres) SearchSemantic(ctx context.Context, project string, query []float32, limit int) ([]core.SearchHit, error) {
	if limit <= 0 {
		limit = 10
	}
	var rows pgx.Rows
	var err error
	if project != "" {
		rows, err = p.pool.Query(ctx, `
SELECT id,project,title,body,frontmatter,tags,content_hash,created,updated,
       1 - (embedding <=> $1) AS score
FROM notes
WHERE embedding IS NOT NULL AND project=$3
ORDER BY embedding <=> $1
LIMIT $2`, pgvector.NewVector(query), limit, project)
	} else {
		rows, err = p.pool.Query(ctx, `
SELECT id,project,title,body,frontmatter,tags,content_hash,created,updated,
       1 - (embedding <=> $1) AS score
FROM notes
WHERE embedding IS NOT NULL
ORDER BY embedding <=> $1
LIMIT $2`, pgvector.NewVector(query), limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []core.SearchHit
	for rows.Next() {
		var (
			n        core.Note
			fm, tags []byte
			score    float64
		)
		if err := rows.Scan(&n.ID, &n.Project, &n.Title, &n.Body, &fm, &tags, &n.ContentHash, &n.Created, &n.Updated, &score); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(fm, &n.Frontmatter)
		_ = json.Unmarshal(tags, &n.Tags)
		hits = append(hits, core.SearchHit{Note: n, Score: score, Kind: "semantic"})
	}
	return hits, rows.Err()
}

func (p *Postgres) KeywordSearch(ctx context.Context, project, q string, limit int) ([]core.Note, error) {
	if limit <= 0 {
		limit = 20
	}
	var rows pgx.Rows
	var err error
	if project != "" {
		rows, err = p.pool.Query(ctx, `
SELECT id,project,title,body,frontmatter,tags,content_hash,created,updated
FROM notes
WHERE (to_tsvector('english', title || ' ' || body) @@ plainto_tsquery('english', $1)
   OR lower(title) LIKE '%' || lower($1) || '%') AND project=$3
ORDER BY updated DESC
LIMIT $2`, q, limit, project)
	} else {
		rows, err = p.pool.Query(ctx, `
SELECT id,project,title,body,frontmatter,tags,content_hash,created,updated
FROM notes
WHERE to_tsvector('english', title || ' ' || body) @@ plainto_tsquery('english', $1)
   OR lower(title) LIKE '%' || lower($1) || '%'
ORDER BY updated DESC
LIMIT $2`, q, limit)
	}
	if err != nil {
		return nil, err
	}
	return p.scanMany(rows)
}

func (p *Postgres) Backlinks(ctx context.Context, idOrTitle string) ([]core.Backlink, error) {
	key := strings.ToLower(strings.TrimSpace(idOrTitle))
	rows, err := p.pool.Query(ctx, `
SELECT n.id, n.title FROM links l
JOIN notes n ON n.id = l.src
WHERE l.dst = $1 OR l.dst = (SELECT lower(title) FROM notes WHERE id=$2)
ORDER BY n.title`, key, idOrTitle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Backlink
	for rows.Next() {
		var b core.Backlink
		if err := rows.Scan(&b.ID, &b.Title); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
func orEmptySlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
