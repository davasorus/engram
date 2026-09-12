# Wiring engram into the Go agent

This document gives the rollout order, the decisions already made, and the
exact config and prompt blocks to add to the agent. Do these steps in
order. Each step proves one layer, so a later failure can only come from
the newest layer.

## 0. Prerequisites (once)

- LM Studio (Windows) has the model `text-embedding-nomic-embed-text-v1.5`
  downloaded. JIT model loading works; the first embed call loads the
  model. The model has 768 dimensions, which matches the `ENGRAM_DIMS`
  default.
- Enable Dependabot alerts and security updates in the GitHub repository
  settings.

## 1. Deploy on Lazerus

```bash
cp compose/.env.example compose/.env
# Set the embed URL to the Windows-host gateway and port-proxy port:
sed -i '/^ENGRAM_EMBED_URL=/d' compose/.env
echo "ENGRAM_EMBED_URL=http://$(ip route show default | awk '{print $3}'):1235" >> compose/.env
podman-compose -f compose/compose.yml up -d   # pulls ghcr.io/davasorus/engram:${ENGRAM_VERSION}
```

The default `.env.example` sets `ENGRAM_MCP_TOOLS=mem_search,mem_read,mem_write`.
This is the reduced tool surface intended for the agent.

## 2. Run the smoke test (before the agent uses engram)

```bash
ENGRAM_URL=http://localhost:8088 ./scripts/smoke.sh
```

All steps must report PASS, including `embedder reachable`. Then check
persistence: run `podman-compose -f compose/compose.yml restart` and run
the smoke test again. Note counts must survive, because state lives in the
`pgdata` volume.

## 3. Degraded mode

engram stays useful when LM Studio is down.

- **Writes succeed** without a vector. The system logs this and the note
  stays keyword-searchable.
- **Semantic search falls back** to keyword search automatically.
- `GET /api/health` reports `missing_vectors`. `GET /api/health?probe=1`
  also tests the embedder.
- `POST /api/reembed` backfills missing vectors once LM Studio comes back.
  Add `?all=1` to re-embed every note; use this only after you change the
  embedding model.

Practical habit: when LM Studio comes back up, run `curl -X POST
http://localhost:8088/api/reembed`.

## 4. Configure the agent (`~/.agent/config.json`)

Use streamable HTTP, not stdio. engram runs as a long-lived shared service:
memory persists across agent sessions, and the web UI and REST share the
same store. It is not a per-session subprocess. Add this block to the MCP
servers section. Adapt the field names to match the agent's schema.

```json
{
  "mcpServers": {
    "engram": {
      "type": "http",
      "url": "http://localhost:8088/mcp"
    }
  }
}
```

With the allowlist above, the agent sees exactly three tools:

| tool         | use                                        |
|--------------|--------------------------------------------|
| `mem_search` | semantic/keyword lookup, returns ranked notes |
| `mem_read`   | fetch full note body by id                 |
| `mem_write`  | create/update a note (upsert by slug id)   |

`mem_patch`, `mem_list`, `mem_links`, `mem_delete`, and
`mem_suggest_links` stay reachable through REST and the web UI. These are
human and maintenance operations.
Smaller models, such as Gemma-12B-class models, call tools more reliably
when they have fewer tools to choose from.

## 5. Add a system prompt block (agent side)

Conventions matter more than plumbing. Use this block as a starting point:

```
MEMORY (engram tools):
- At the START of a task, mem_search for relevant prior notes before asking
  the user or re-deriving facts.
- mem_write durable knowledge only: decisions made, facts about this
  environment/project, solutions that took effort. Never write chatter,
  transient state, or step-by-step logs.
- Titles are short noun phrases ("Proj9 Liquibase bootstrap pattern");
  the note id is the slugified title, and writing the same title updates
  that note rather than creating a duplicate.
- Always include the tag "source:agent" plus 1-3 topical tags.
- Use [[wikilinks]] in bodies to connect related notes.
```

The `source:agent` tag is the cleanup lever. It keeps agent-written notes
distinguishable from human-written notes at all times. Adopting this tag
now costs much less than migrating tags later.

## 6. Verify the setup end to end

Give the agent a task such as: "Search your memory for notes about
Liquibase. If nothing is found, write a note titled 'Engram integration
test' tagged source:agent." Then confirm in the web UI
(http://localhost:8088) that the note exists with the tag. Run
`mem_search` again in a fresh session to confirm it finds the note.

## Security posture

REST and MCP have no authentication by design; this fits a single-user
homelab. Keep the port bound to localhost or the LAN. Do not forward port
8088 through anything public. If that requirement changes, add auth in
front of engram, for example through a reverse proxy, before you expose
the service.
