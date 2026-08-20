# engram — design

**engram** is a self-contained memory service for an AI agent: a brain, not an
Obsidian clone. It stores markdown notes *as data* (not files), indexes them for
**semantic search** (vector embeddings) and **structured/link** queries, and
exposes everything over **MCP** (for the agent) and a **REST API + web UI** (for
humans and scripts) — all from a single Go process in a single container that
owns all of its state.

## Why it's shaped this way

Tonight's lesson: shared filesystems and network shims between the agent and its
memory are fragile (WSL↔Windows mounts, an Obsidian REST plugin over TLS across
a portproxy). engram removes that entire class of failure:

- **The container owns its data.** No host bind mounts of note files; nothing
  is shared between host and container, so there's nothing to coordinate or
  corrupt. State lives in the Postgres volume — the single unit you back up or
  move (Lazerus Podman today, managed cloud Postgres later).
- **Notes are records, not files.** A note's markdown body is a column, not a
  `.md` on disk. This collapses the three conceptual layers — markdown content,
  structured metadata/links, and vectors — into one store with one write path,
  eliminating file-drift and two-writer hazards.
- **One process, two interfaces.** MCP and REST are thin adapters over one core
  library, so they can't drift apart.

## Storage: Postgres + pgvector, behind a `Store` interface

The core depends on a `Store` interface, not on the database directly. The
implementation (`internal/store`) is **Postgres + pgvector**: notes are rows,
the markdown body is a column, and embeddings are a `vector(N)` column with an
**HNSW cosine index** so KNN runs in SQL and scales past brute force. Keyword
search uses a GIN full-text index as a complementary exact-match path.

Postgres runs as its own container in the compose/kube stack; engram connects
over a normal DSN (`jackc/pgx`) and connects with a short retry loop so start
order doesn't matter. Because storage is behind the interface, an embedded or
alternative backend could be added later without changing anything above it.

An earlier iteration used embedded SQLite for a single-file artifact; we moved
to Postgres+pgvector for mature, indexed vector search and a clean path to
managed cloud databases, accepting a two-service stack in exchange.

## Data model

A note record:

| field         | meaning                                                        |
|---------------|----------------------------------------------------------------|
| `id`          | stable slug/uuid                                               |
| `title`       | display title                                                  |
| `body`        | the markdown text (source of truth, served verbatim)           |
| `frontmatter` | parsed YAML/JSON metadata (tags, type, etc.)                   |
| `links`       | outgoing `[[wikilinks]]` (edges; backlinks derived by reverse) |
| `content_hash`| hash of body; lets us skip re-embedding unchanged notes        |
| `vector`      | embedding of the body (BLOB of float32)                        |
| `created`/`updated` | timestamps                                               |

Links are first-class: parsed on write, stored as edges, backlinks computed by
reverse lookup.

## Intelligence

- **Embeddings** come from an OpenAI-compatible embeddings endpoint — by default
  the user's local LM Studio (`text-embedding-nomic-embed-text-v1.5`), so the
  whole system stays local. Configurable.
- **Semantic search**: embed the query, cosine-rank all note vectors, return the
  top-k with scores; resolvable to full markdown.
- **Keyword search**: Postgres GIN full-text as a complementary exact-match path.
- **Sync**: because content lives in the DB, the write path updates body +
  hash + vector atomically — there is no separate file to drift. A `reembed`
  operation can rebuild vectors (e.g. after changing embedding models).

## Interfaces (one process)

- **MCP** (stdio + streamable HTTP): tools `mem_search`, `mem_read`,
  `mem_write`, `mem_patch`, `mem_links`, `mem_list`, `mem_delete`.
- **REST**: `GET/POST/PATCH/DELETE /api/notes`, `GET /api/search`,
  `GET /api/notes/{id}/links`, `POST /api/reembed`.
- **Web UI**: server-rendered markdown → HTML (goldmark: CommonMark + GFM,
  extended for `[[wikilinks]]` and callouts), with mermaid + math (KaTeX)
  rendered client-side. Read/browse/search-first, with a live-preview editor
  and wikilink autocomplete for the occasional hand-edit. It's a window into the
  agent's brain, not a full PKM suite.

## Packaging

Standalone project: Go service + a Postgres container, container image, compose stack, and
kube manifests. Because the container owns its state as one DB file, it runs the
same on local Podman or a cloud host; persistence is the Postgres data volume.
