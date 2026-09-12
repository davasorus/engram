# engram — design

engram is a self-contained memory service for an AI agent. It is a brain,
not an Obsidian clone. It stores markdown notes as data, not as files. It
indexes notes for semantic search with vector embeddings. It indexes notes
for structured and link queries. It exposes all functions over MCP for the
agent. It exposes all functions over a REST API and a web UI for humans and
scripts. One Go process in one container owns all of this state.

## Why the system has this shape

An earlier setup relied on shared filesystems and network shims between the
agent and its memory. That setup was fragile: WSL-to-Windows mounts, and an
Obsidian REST plugin over TLS across a port proxy. engram removes this class
of failure.

- **The container owns its data.** No host bind mounts hold note files.
  Nothing is shared between host and container. There is nothing to
  coordinate or corrupt. State lives in the Postgres volume, the single unit
  you back up or move (Podman today, managed cloud Postgres later).
- **Notes are records, not files.** A note's markdown body is a column, not
  a `.md` file on disk. This design collapses three conceptual layers —
  markdown content, structured metadata and links, and vectors — into one
  store with one write path. It removes file drift and two-writer hazards.
- **One process, two interfaces.** MCP and REST are thin adapters over one
  core library. The two interfaces cannot drift apart.

## Storage: Postgres and pgvector, behind a Store interface

The core depends on a `Store` interface, not on the database directly. The
implementation (`internal/store`) uses Postgres and pgvector. Notes are
rows. The markdown body is a column. Embeddings use a `vector(N)` column
with an HNSW cosine index, so KNN search runs in SQL and scales past brute
force. Keyword search uses a GIN full-text index as a complementary
exact-match path.

The engine also supports a hybrid search mode (`kind=hybrid`). Hybrid
search runs the semantic and keyword queries side by side, then merges
their ranked lists with Reciprocal Rank Fusion (RRF). RRF sums, for each
note, one term per list it appears in: 1 divided by a constant plus the
note's rank in that list. This method combines two scores on different
scales, cosine similarity and text-search relevance, without normalizing
either one. A note that ranks well in both searches moves to the top. A
note that only one search finds still appears, ranked lower.

Postgres runs as its own container in the compose and kube stack. engram
connects over a normal DSN through `jackc/pgx`, with a short retry loop so
start order does not matter. Because storage sits behind the interface, a
future release could add an embedded or alternative backend without
changing anything above it.

An earlier iteration used embedded SQLite for a single-file artifact. The
project moved to Postgres and pgvector for mature, indexed vector search
and a clean path to managed cloud databases. This trade accepts a
two-service stack in exchange.

## Data model

A note record:

| field         | meaning                                                        |
|---------------|----------------------------------------------------------------|
| `id`          | stable slug or UUID                                             |
| `title`       | display title                                                  |
| `body`        | the markdown text (source of truth, served verbatim)           |
| `frontmatter` | parsed YAML/JSON metadata (tags, type, etc.)                   |
| `links`       | outgoing `[[wikilinks]]` (edges; backlinks derived by reverse) |
| `content_hash`| hash of the body; lets the store skip re-embedding unchanged notes |
| `vector`      | embedding of the body (BLOB of float32)                        |
| `created`/`updated` | timestamps                                               |

Links are first-class. The write path parses links and stores them as
edges. The store computes backlinks by reverse lookup.

## Intelligence

- **Embeddings** come from an OpenAI-compatible embeddings endpoint. By
  default this is the user's local LM Studio instance
  (`text-embedding-nomic-embed-text-v1.5`), so the whole system stays local.
  You can configure a different endpoint.
- **Embedding fallback**: `ENGRAM_EMBED_URL` accepts a comma-separated list
  of base URLs. The client tries them in order and falls back to the next
  one when a call fails. It remembers the last endpoint that worked and
  tries that one first next time. This lets a secondary provider stand in
  when the primary is down, for example when a local LM Studio instance
  restarts.
- **Semantic search**: the engine embeds the query, ranks all note vectors
  by cosine similarity, and returns the top-k results with scores,
  resolvable to full markdown.
- **Keyword search**: Postgres GIN full-text search acts as a complementary
  exact-match path.
- **Sync**: because content lives in the database, the write path updates
  the body, hash, and vector atomically. There is no separate file to
  drift. A reembed operation can rebuild vectors, for example after you
  change embedding models. By default, `POST /api/reembed` only backfills
  notes with no vector yet (written while the embedder was down); add
  `?full=1` to rebuild every note's vector unconditionally, for example
  after a model change. The CLI mirrors this with `engram reembed [-full]`.
- **Cross-link suggestions**: `mem_suggest_links` (and
  `GET /api/notes/{id}/suggestions`) reuse the note's own vector to find
  other notes that are semantically close but not yet linked. This gives
  an agent a proactive cross-linking aid without a separate completion
  call. It does not generate a written summary.
- **Summarization**: `mem_summarize` (and `GET /api/notes/{id}/summary`)
  send the note body to an optional chat-completion endpoint and return
  a short written summary. This feature is opt-in. Set `--complete-url`
  (or `ENGRAM_COMPLETE_URL`) and `--complete-model` to enable it. Without
  a configured endpoint, engram returns a 501 error for both the tool
  call and the REST endpoint. The `internal/complete` package mirrors
  `internal/embed`: it talks to an OpenAI-compatible
  `/v1/chat/completions` endpoint, and it accepts the same
  comma-separated fallback list.

## Interfaces (one process)

- **MCP** (stdio and streamable HTTP): tools `mem_search`, `mem_read`,
  `mem_write`, `mem_patch`, `mem_links`, `mem_list`, `mem_delete`,
  `mem_suggest_links`, `mem_summarize`.
- **REST**: `GET/POST/PATCH/DELETE /api/notes`, `GET /api/search`,
  `GET /api/notes/{id}/links`, `GET /api/notes/{id}/suggestions`,
  `GET /api/notes/{id}/summary`, `POST /api/reembed`.
- **Web UI**: the server renders markdown to HTML with goldmark
  (CommonMark and GFM, extended for `[[wikilinks]]` and callouts). The
  client renders mermaid diagrams and math (KaTeX). The UI favors
  reading, browsing, and search, with a live-preview editor and wikilink
  autocomplete for occasional hand edits. It is a window into the agent's
  brain, not a full PKM suite.

## Command-line client

The `engram` binary also acts as a REST client against a running server.
A recognized subcommand as the first argument, for example `engram search
foo`, switches the binary to client mode. Any other invocation, for
example `engram -addr :9000`, starts the server as before. `internal/
cliclient` holds the HTTP calls; each subcommand in `cmd/engram/cli.go`
issues one REST call and prints JSON or a short message. This gives a
human a terminal-based way to manage, inspect, or debug notes, without
the web UI or hand-written `curl` calls.

Subcommands: `health`, `search`, `list`, `get`, `write`, `patch`,
`delete`, `links`, `suggest`, `summary`, `reembed`. Each accepts a
`-server` flag (default `http://localhost:8088`, or `ENGRAM_CLI_SERVER`).
Run `engram help` for the full list with per-command options.

## Packaging

engram is a standalone project: a Go service and a Postgres container, a
container image, a compose stack, and kube manifests. Because the
container owns its state as one database, it runs the same way on local
Podman or on a cloud host. Persistence lives in the Postgres data volume.
