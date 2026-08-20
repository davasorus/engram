// Package core holds engram's domain model and the interfaces the rest of the
// system depends on. Storage (SQLite today, maybe Postgres later) and
// embeddings (LM Studio today, anything OpenAI-compatible) are both behind
// interfaces so the engine, MCP, REST, and web layers never depend on a
// concrete backend.
package core

import (
	"context"
	"time"
)

// Note is a single memory record. The markdown body is the source of truth;
// everything else (links, vector, hash) is derived from it on write.
type Note struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Body        string         `json:"body"` // markdown text
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
	Links       []string       `json:"links,omitempty"` // outgoing [[wikilinks]]
	Tags        []string       `json:"tags,omitempty"`
	ContentHash string         `json:"content_hash,omitempty"`
	Created     time.Time      `json:"created"`
	Updated     time.Time      `json:"updated"`
	// Vector is not serialized to JSON by default; it's an internal index.
	Vector []float32 `json:"-"`
}

// SearchHit is a note plus its relevance to a query.
type SearchHit struct {
	Note  Note    `json:"note"`
	Score float64 `json:"score"` // cosine similarity (semantic) or 1.0 (keyword)
	Kind  string  `json:"kind"`  // "semantic" | "keyword"
}

// Backlink is a note that links TO the note being inspected.
type Backlink struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Store is the persistence contract. The SQLite implementation lives in
// internal/store; a Postgres one could be added without changing callers.
//
// Implementations must be safe for concurrent reads. engram is single-writer
// by design (the service owns its state), so writes need not be internally
// serialized beyond what the backend already guarantees.
type Store interface {
	// Upsert creates or replaces a note (by ID). The caller supplies a fully
	// populated Note including Vector, Links, ContentHash.
	Upsert(ctx context.Context, n Note) error

	// Get returns a note by ID, or (nil, nil) if not found.
	Get(ctx context.Context, id string) (*Note, error)

	// Delete removes a note by ID. Missing is not an error.
	Delete(ctx context.Context, id string) error

	// List returns notes ordered by Updated desc, paginated.
	List(ctx context.Context, limit, offset int) ([]Note, error)

	// Count returns the total number of notes.
	Count(ctx context.Context) (int, error)

	// MissingVectorIDs returns IDs of notes with no stored embedding
	// (created by degraded writes while the embedder was unreachable).
	MissingVectorIDs(ctx context.Context) ([]string, error)

	// SearchSemantic runs vector KNN in the database and returns ranked hits.
	// The backend does the ranking (e.g. pgvector HNSW), so this scales
	// without pulling all vectors into the app.
	SearchSemantic(ctx context.Context, query []float32, limit int) ([]SearchHit, error)

	// KeywordSearch does a full-text/substring match over title+body.
	KeywordSearch(ctx context.Context, q string, limit int) ([]Note, error)

	// Backlinks returns notes whose Links contain the given id/title.
	Backlinks(ctx context.Context, idOrTitle string) ([]Backlink, error)

	// Close releases resources.
	Close() error
}

// Embedder turns text into a vector. The default implementation calls an
// OpenAI-compatible /v1/embeddings endpoint (LM Studio).
type Embedder interface {
	// Embed returns the embedding for a single piece of text.
	Embed(ctx context.Context, text string) ([]float32, error)
	// Model reports the model id, recorded so a model change can trigger
	// re-embedding.
	Model() string
}
