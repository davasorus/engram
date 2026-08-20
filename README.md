# engram

![ci](https://github.com/davasorus/engram/actions/workflows/ci.yml/badge.svg)

A containerized **memory service for an AI agent** — a brain, not an Obsidian
clone. It stores markdown notes *as data* (Postgres, not files), indexes them
for **semantic search** (pgvector) and **link/structure** queries, and exposes
everything over **MCP** (for the agent) and a **REST API + web UI** (for humans
and scripts) — from a single Go service that runs alongside its database as a
declared container stack.

## Why it's shaped this way

The agent's memory used to depend on a chain of fragile pieces (a note-taking
desktop app, a plugin HTTP server, TLS, an OS filesystem bridge). engram
replaces that with a normal service + database:

- **Notes are records, not files.** A note's markdown body is a column. This
  unifies the three layers — markdown content, structured metadata/links, and
  vectors — into one store with one write path. No shared filesystem between
  host and container, so there's nothing to coordinate or corrupt.
- **Real vector search.** Embeddings live in Postgres via **pgvector**, with an
  HNSW index; KNN runs in SQL and scales past brute force.
- **One service, two interfaces.** MCP and REST are thin adapters over one core
  engine, so they can't drift apart.
- **Declared stack.** engram + Postgres are both containers in compose/kube —
  reproducible, and hostable locally (Podman) or on managed cloud Postgres.

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

## Storage: Postgres + pgvector, behind a `Store` interface

The core depends on `core.Store`, not on Postgres directly (`internal/store`
holds the pgvector implementation). Vectors are a `vector(N)` column with an
HNSW cosine index; keyword search uses a GIN full-text index as a fallback.

## Quick start (compose)

```bash
cp compose/.env.example compose/.env
$EDITOR compose/.env          # set POSTGRES_PASSWORD, ENGRAM_DIMS, embed URL
podman-compose -f compose/compose.yml up -d --build
# UI:   http://localhost:8088/
# MCP:  http://localhost:8088/mcp/
# REST: http://localhost:8088/api/
```

`ENGRAM_DIMS` **must** match your embedding model's output width
(nomic-embed-text-v1.5 = 768).

## Quick start (Kubernetes-shaped)

```bash
podman play kube kube/engram.yaml       # runs on Podman
# or: kubectl apply -f kube/engram.yaml  # on a real cluster
```

## Pointing the agent at it

Configure the agent's MCP client with an HTTP server at
`http://<host>:8088/mcp/`. Tools exposed: `mem_search`, `mem_read`,
`mem_write`, `mem_patch`, `mem_links`, `mem_list`, `mem_delete`.

## Interfaces

- **MCP** — `POST /mcp/` (streamable HTTP), or run `engram -stdio` for a
  subprocess transport.
- **REST** — `GET /api/search?q=&kind=`, `GET|POST /api/notes`,
  `GET|PATCH|DELETE /api/notes/{id}`, `GET /api/notes/{id}/links`,
  `POST /api/reembed`, `GET /api/health`.
- **Web UI** — browse, view (rendered markdown: GFM, callouts, mermaid, KaTeX),
  semantic/keyword search, and a live-preview editor with wikilink autocomplete.

## Dependencies (community/standard, not hand-rolled)

- `jackc/pgx` + `pgvector/pgvector-go` — Postgres driver and vector types.
- `yuin/goldmark` (+ GFM) — markdown rendering.
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
| `internal/web` | web UI + markdown rendering |
| `compose/`, `kube/` | the declared container stack |

## Notes

- Changing embedding models with a different width requires migrating the
  `vector(N)` column and re-embedding (`POST /api/reembed`).
- The `pgdata` volume is the backup unit — the whole vault lives there.

## Development

```bash
go test ./...                                   # unit tests (no container needed)
go test -tags=integration ./internal/store/...  # store tests (spins up pgvector via testcontainers; needs Docker/Podman)
golangci-lint run                               # lint
gofmt -l .                                      # format check
```

CI (`.github/workflows/ci.yml`) runs build, vet, golangci-lint, gofmt check, unit
tests, and the pgvector integration tests on every push/PR.

## Releases

Pushing a semver tag builds and publishes via GoReleaser
(`.github/workflows/release.yml`): multi-arch binaries + a GitHub Release, and a
multi-arch container image to **GHCR** at `ghcr.io/davasorus/engram`.

```bash
git tag v0.1.0 && git push origin v0.1.0
podman pull ghcr.io/davasorus/engram:0.1.0
```
