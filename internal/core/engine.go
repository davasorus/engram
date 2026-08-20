package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

// Engine is the single source of business logic. MCP, REST, and the web UI are
// all thin adapters over this — so the three interfaces can never drift apart.
type Engine struct {
	store    Store
	embedder Embedder
}

func NewEngine(s Store, e Embedder) *Engine {
	return &Engine{store: s, embedder: e}
}

var (
	wikilinkRe = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]+)?\]\]`)
	slugRe     = regexp.MustCompile(`[^a-z0-9]+`)
)

// WriteInput is what callers supply to create/update a note. The engine derives
// links, hash, and vector.
type WriteInput struct {
	ID          string         // optional; derived from title if empty
	Title       string         // required
	Body        string         // markdown
	Frontmatter map[string]any // optional
	Tags        []string       // optional
}

// Write creates or updates a note: parses wikilinks, hashes the body, embeds
// it (skipping re-embed when the body is unchanged), and persists.
func (e *Engine) Write(ctx context.Context, in WriteInput) (*Note, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	id := in.ID
	if id == "" {
		id = slug(in.Title)
	}
	hash := hashBody(in.Body)

	// Reuse the existing vector if the body is byte-identical (avoids a
	// needless embed round-trip on metadata-only edits).
	var vector []float32
	existing, _ := e.store.Get(ctx, id)
	if existing != nil && existing.ContentHash == hash && len(existing.Vector) > 0 {
		vector = existing.Vector
	} else if v, err := e.embedder.Embed(ctx, embedText(in.Title, in.Body)); err == nil {
		vector = v
	} else {
		// Degraded write: the embedder is unreachable (e.g. LM Studio is
		// down). Store the note without a vector — it stays keyword-
		// searchable — and let a reembed backfill it later. /api/health
		// reports missing_vectors; POST /api/reembed fixes them.
		log.Printf("engram: embed failed for %q, storing without vector: %v", id, err)
		vector = nil
	}

	n := Note{
		ID:          id,
		Title:       in.Title,
		Body:        in.Body,
		Frontmatter: in.Frontmatter,
		Tags:        in.Tags,
		Links:       parseLinks(in.Body),
		ContentHash: hash,
		Vector:      vector,
	}
	if existing != nil {
		n.Created = existing.Created
	} else {
		n.Created = time.Now().UTC()
	}
	if err := e.store.Upsert(ctx, n); err != nil {
		return nil, err
	}
	return e.store.Get(ctx, id)
}

// Patch replaces the first exact occurrence of oldStr with newStr in a note's
// body, then re-derives links/hash/vector. Errors if oldStr is absent or
// ambiguous (appears more than once) — mirroring the agent's file-edit
// semantics so behavior is predictable.
func (e *Engine) Patch(ctx context.Context, id, oldStr, newStr string) (*Note, error) {
	n, err := e.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, fmt.Errorf("note %q not found", id)
	}
	count := strings.Count(n.Body, oldStr)
	if count == 0 {
		return nil, fmt.Errorf("old_str not found in note %q", id)
	}
	if count > 1 {
		return nil, fmt.Errorf("old_str appears %d times in note %q; make it unique", count, id)
	}
	newBody := strings.Replace(n.Body, oldStr, newStr, 1)
	return e.Write(ctx, WriteInput{
		ID: n.ID, Title: n.Title, Body: newBody,
		Frontmatter: n.Frontmatter, Tags: n.Tags,
	})
}

func (e *Engine) Read(ctx context.Context, id string) (*Note, error) {
	return e.store.Get(ctx, id)
}

func (e *Engine) Delete(ctx context.Context, id string) error {
	return e.store.Delete(ctx, id)
}

func (e *Engine) List(ctx context.Context, limit, offset int) ([]Note, error) {
	return e.store.List(ctx, limit, offset)
}

func (e *Engine) Count(ctx context.Context) (int, error) {
	return e.store.Count(ctx)
}

func (e *Engine) Backlinks(ctx context.Context, idOrTitle string) ([]Backlink, error) {
	return e.store.Backlinks(ctx, idOrTitle)
}

// Search runs semantic search by default; when semantic is unavailable (no
// vectors yet) or kind=="keyword", it falls back to keyword matching.
func (e *Engine) Search(ctx context.Context, query string, limit int, kind string) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 10
	}
	if kind == "keyword" {
		return e.keyword(ctx, query, limit)
	}
	// semantic
	qv, err := e.embedder.Embed(ctx, query)
	if err != nil {
		// Embedding unavailable (e.g. LM Studio down): degrade to keyword.
		return e.keyword(ctx, query, limit)
	}
	return e.store.SearchSemantic(ctx, qv, limit)
}

func (e *Engine) keyword(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	notes, err := e.store.KeywordSearch(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(notes))
	for _, n := range notes {
		hits = append(hits, SearchHit{Note: n, Score: 1.0, Kind: "keyword"})
	}
	return hits, nil
}

// Reembed rebuilds vectors. With onlyMissing it backfills just the notes
// that have no vector (from degraded writes while the embedder was down);
// otherwise it re-embeds every note (e.g. after an embedding-model change).
// Returns the number re-embedded.
func (e *Engine) Reembed(ctx context.Context, onlyMissing bool) (int, error) {
	if onlyMissing {
		ids, err := e.store.MissingVectorIDs(ctx)
		if err != nil {
			return 0, err
		}
		var n int
		for _, id := range ids {
			note, err := e.store.Get(ctx, id)
			if err != nil {
				return n, err
			}
			if note == nil {
				continue
			}
			if err := e.reembedOne(ctx, *note); err != nil {
				return n, err
			}
			n++
		}
		return n, nil
	}
	var n int
	offset := 0
	for {
		batch, err := e.store.List(ctx, 100, offset)
		if err != nil {
			return n, err
		}
		if len(batch) == 0 {
			break
		}
		for _, note := range batch {
			if err := e.reembedOne(ctx, note); err != nil {
				return n, err
			}
			n++
		}
		offset += len(batch)
	}
	return n, nil
}

func (e *Engine) reembedOne(ctx context.Context, note Note) error {
	v, err := e.embedder.Embed(ctx, embedText(note.Title, note.Body))
	if err != nil {
		return fmt.Errorf("reembed %q: %w", note.ID, err)
	}
	note.Vector = v
	note.Links = parseLinks(note.Body)
	note.ContentHash = hashBody(note.Body)
	return e.store.Upsert(ctx, note)
}

// MissingVectors reports how many notes have no embedding (degraded writes
// awaiting backfill).
func (e *Engine) MissingVectors(ctx context.Context) (int, error) {
	ids, err := e.store.MissingVectorIDs(ctx)
	return len(ids), err
}

// ProbeEmbedder checks whether the embedding endpoint is reachable.
func (e *Engine) ProbeEmbedder(ctx context.Context) error {
	_, err := e.embedder.Embed(ctx, "health probe")
	return err
}

// --- helpers ----------------------------------------------------------------

func parseLinks(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range wikilinkRe.FindAllStringSubmatch(body, -1) {
		t := strings.ToLower(strings.TrimSpace(m[1]))
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func slug(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = fmt.Sprintf("note-%d", time.Now().UnixNano())
	}
	return s
}

func hashBody(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

// embedText combines title and body so the title contributes to the embedding.
func embedText(title, body string) string {
	return title + "\n\n" + body
}
