# Wiring engram into the Go agent

The rollout order, the decisions already made, and the exact config/prompt
blocks to drop into the agent. Do these in order — each step proves a layer
so later failures can only be in the newest layer.

## 0. Prerequisites (once)

- LM Studio (Windows) has `text-embedding-nomic-embed-text-v1.5` downloaded.
  JIT model loading is fine; the first embed call loads it. 768 dims — matches
  `ENGRAM_DIMS` default.
- Dependabot alerts + security updates enabled in the GitHub repo settings.

## 1. Deploy on Lazerus

```bash
cp compose/.env.example compose/.env
# Set the embed URL to the Windows-host gateway + portproxy port:
sed -i '/^ENGRAM_EMBED_URL=/d' compose/.env
echo "ENGRAM_EMBED_URL=http://$(ip route show default | awk '{print $3}'):1235" >> compose/.env
podman-compose -f compose/compose.yml up -d --build
```

The default `.env.example` sets `ENGRAM_MCP_TOOLS=mem_search,mem_read,mem_write`
— the reduced surface intended for the agent.

## 2. Smoke test (before the agent touches it)

```bash
ENGRAM_URL=http://localhost:8088 ./scripts/smoke.sh
```

All steps must PASS, including `embedder reachable`. Then the persistence
check: `podman-compose -f compose/compose.yml restart` and re-run the smoke —
note counts survive because state lives in the `pgdata` volume.

## 3. Degraded mode (know it, don't fear it)

engram stays useful when LM Studio is down:

- **Writes succeed** without a vector (logged; note is keyword-searchable).
- **Semantic search falls back** to keyword automatically.
- `GET /api/health` reports `missing_vectors`; `GET /api/health?probe=1`
  additionally tests the embedder.
- `POST /api/reembed` backfills missing vectors once LM Studio is back
  (`?all=1` re-embeds everything — only needed after changing embed models).

Practical habit: when LM Studio comes back up, `curl -X POST
http://localhost:8088/api/reembed`.

## 4. Agent config (`~/.agent/config.json`)

Streamable HTTP, not stdio — engram is a long-lived shared service (memory
persists across agent sessions; the web UI and REST share the same store),
not a per-session subprocess. Add to the MCP servers section (adapt field
names to the agent's schema):

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

`mem_patch`, `mem_list`, `mem_links`, `mem_delete` stay reachable via REST
and the web UI — they're human/maintenance operations, and Gemma-12B-class
models call tools more reliably with fewer to choose from.

## 5. System prompt addition (agent side)

Conventions matter more than plumbing. Suggested block:

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

The `source:agent` tag is the cleanup lever: it keeps agent-written notes
distinguishable from human-written ones forever, and it's much cheaper to
adopt now than to migrate in later.

## 6. Verify end-to-end

Give the agent a task like: "Search your memory for notes about Liquibase.
If nothing is found, write a note titled 'Engram integration test' tagged
source:agent." Then confirm in the web UI (http://localhost:8088) that the
note exists with the tag, and `mem_search` finds it in a fresh session.

## Security posture

No auth on REST/MCP by design (single-user homelab). Keep the port bound to
localhost/LAN; do not forward 8088 through anything public. If that changes,
auth goes in front (reverse proxy) before exposure.
