# engram

![ci](https://github.com/davasorus/engram/actions/workflows/ci.yml/badge.svg)

engram is a containerized memory service for an AI agent. It is a brain, not
an Obsidian clone. It stores markdown notes as data in Postgres, not as
files. It indexes notes for semantic search with pgvector. It indexes notes
for link and structure queries. It exposes all functions over MCP for the
agent. It exposes all functions over a REST API and a web UI for humans and
scripts. A single Go service runs these functions alongside its database as
a declared container stack.

## Why the system has this shape

The agent's memory used to depend on a chain of fragile parts: a note-taking
desktop app, a plugin HTTP server, TLS, and an OS filesystem bridge. engram
replaces that chain with a normal service and a database.

- **Notes are records, not files.** A note's markdown body is a column. This
  design unifies three layers — markdown content, structured metadata and
  links, and vectors — into one store with one write path. No filesystem is
  shared between host and container. There is nothing to coordinate or
  corrupt.
- **Real vector search.** Embeddings live in Postgres through pgvector, with
  an HNSW index. KNN search runs in SQL and scales past brute force.
- **One service, two interfaces.** MCP and REST are thin adapters over one
  core engine. The two interfaces cannot drift apart.
- **Declared stack.** engram and Postgres are both containers in compose and
  kube files. The stack is reproducible. You can host it locally with Podman
  or on a managed cloud Postgres instance.

## Architecture

```
                 ┌───────────────────────────────┐
   agent ──MCP──▶│  engram (Go)                  │
  scripts ─REST─▶│   core engine ── embeds via ──┼──▶ LM Studio (/v1/embeddings)
   you ────UI───▶│   MCP + REST + web adapters   │
                 └──────────────┬────────────────┘
                                │ pgx
                       ┌────────▼─────────┐
                       │ Postgres+pgvector│  (its own container / PVC)
                       └──────────────────┘
```

## Storage: Postgres and pgvector, behind a Store interface

The core depends on the `core.Store` interface, not on Postgres directly.
`internal/store` holds the pgvector implementation. Vectors use a
`vector(N)` column with an HNSW cosine index. Keyword search uses a GIN
full-text index as a fallback.

## Quick start (compose)

```bash
cd compose && cp .env.example .env   # set POSTGRES_PASSWORD, ENGRAM_DIMS, ENGRAM_EMBED_URL, ENGRAM_VERSION
podman-compose -f compose.yml up -d  # pulls engram + engram-migrate from GHCR

# To develop engram itself, build from source with the overlay:
#   podman-compose -f compose.yml -f Compose.dev.yml up -d --build
# UI:   http://localhost:8088/
# MCP:  http://localhost:8088/mcp/
# REST: http://localhost:8088/api/
```

`ENGRAM_DIMS` must match the output width of your embedding model
(nomic-embed-text-v1.5 = 768).

## Quick start (Kubernetes-shaped)

```bash
podman play kube kube/engram.yaml       # runs on Podman
# or: kubectl apply -f kube/engram.yaml  # on a real cluster
```

## Pointing the agent at engram

Configure the agent's MCP client with an HTTP server at
`http://<host>:8088/mcp/`. engram exposes these tools: `mem_search`,
`mem_read`, `mem_write`, `mem_patch`, `mem_links`, `mem_list`, `mem_delete`,
`mem_suggest_links`, `mem_summarize`, `mem_stats`.

`mem_summarize` needs a chat-completion endpoint. Set `--complete-url` (or
`ENGRAM_COMPLETE_URL`) and `--complete-model` to enable it. Without these
flags, engram returns 501 for summary requests.

## Interfaces

- **MCP** — `POST /mcp/` for streamable HTTP. Run `engram -stdio` for a
  subprocess transport.
- **REST** — `GET /api/search?q=&kind=semantic|keyword|hybrid`, `GET|POST /api/notes`,
  `GET|PATCH|DELETE /api/notes/{id}`, `GET /api/notes/{id}/links`,
  `GET /api/notes/{id}/suggestions`, `GET /api/notes/{id}/summary`,
  `POST /api/reembed`, `GET /api/stats`, `GET /api/health`.
- **Web UI** — browse notes, view rendered markdown (GFM, callouts, mermaid,
  KaTeX), run semantic, keyword, or hybrid search, and edit notes with a
  live-preview editor and wikilink autocomplete.
- **CLI** — the same `engram` binary acts as a REST client when its first
  argument is a subcommand. Point it at a running server and manage notes
  from a terminal:

  ```bash
  engram health
  engram search -kind hybrid "postgres migrations"
  engram write -title "Meeting notes" -body-file notes.md
  engram get meeting-notes
  engram links meeting-notes
  engram suggest meeting-notes
  engram delete meeting-notes
  ```

  Run `engram help` for the full command list. Every subcommand accepts
  `-server` (default `http://localhost:8088`, or `ENGRAM_CLI_SERVER`).

## Dependencies

- `jackc/pgx` and `pgvector/pgvector-go` — Postgres driver and vector types.
- `yuin/goldmark` with GFM — markdown rendering.
- `modelcontextprotocol/go-sdk` — MCP server.
- stdlib `net/http` (1.22 method-pattern mux) — routing.

## Layout

| Path | What |
|------|------|
| `cmd/engram` | entrypoint (HTTP or `-stdio`) |
| `internal/core` | domain model, `Store`/`Embedder` interfaces, engine |
| `internal/store` | Postgres + pgvector implementation |
| `internal/embed` | OpenAI-compatible embeddings client |
| `internal/mcp` | MCP adapter |
| `internal/rest` | REST adapter |
| `internal/web` | web UI and markdown rendering |
| `compose/`, `kube/` | the declared container stack |

## Notes

- If you change the embedding model to a different width, you must migrate
  the `vector(N)` column and re-embed all notes (`POST /api/reembed?full=1`).
  Without `full=1`, reembed only backfills notes that have no vector yet.
- The `pgdata` volume is the backup unit. The whole vault lives there.

## AI usage

This project was created with a combination of online Claude Code and
offline [gemma-4-12B](https://huggingface.co/google/gemma-4-12B).

## Development

```bash
go test ./...                                   # unit tests (no container needed)
go test -tags=integration ./internal/store/...  # store tests (spins up pgvector via testcontainers; needs Docker/Podman)
golangci-lint run                               # lint
gofmt -l .                                      # format check
```

The CI workflow (`.github/workflows/ci.yml`) runs the build, vet,
golangci-lint, the gofmt check, unit tests, and the pgvector integration
tests on every push and pull request.

## Releases

A pushed semver tag triggers `.github/workflows/release.yml`. This workflow
builds and publishes multi-arch binaries and a GitHub release through
GoReleaser. It also builds and publishes two multi-arch container images to
GHCR, tagged `X.Y.Z` (no leading `v`) and `latest`:

- `ghcr.io/davasorus/engram` — the service.
- `ghcr.io/davasorus/engram-migrate` — Liquibase and the schema changelog.
  Run this image before engram to apply migrations. See
  [docs/MIGRATIONS.md](docs/MIGRATIONS.md).

```bash
git tag v0.4.0 && git push origin v0.4.0
podman pull ghcr.io/davasorus/engram:0.4.0
podman pull ghcr.io/davasorus/engram-migrate:0.4.0
```

To upgrade a running deployment to a new release, see
[docs/UPGRADING.md](docs/UPGRADING.md).
