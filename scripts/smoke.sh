#!/usr/bin/env bash
# Smoke test against a RUNNING engram instance. Exercises the full loop:
# health -> write -> read -> semantic search -> keyword search -> backlinks
# -> reembed -> delete. Run after `podman-compose up` and before pointing
# the agent at it.
#
#   ENGRAM_URL=http://localhost:8088 ./scripts/smoke.sh
#
# Exit 0 = every step passed. Uses python3 for JSON parsing (no jq needed).
set -u
URL="${ENGRAM_URL:-http://localhost:8088}"
pass=0; fail=0

check() { # check <name> <expression result: 0/1>
	if [ "$2" -eq 0 ]; then echo "PASS  $1"; pass=$((pass+1)); else echo "FAIL  $1"; fail=$((fail+1)); fi
}

json() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }

echo "== engram smoke @ $URL =="

# 1. Health + embedder probe
h=$(curl -sf "$URL/api/health?probe=1")
check "health endpoint"        $?
echo "      $h"
emb=$(echo "$h" | json "['embedder']")
[ "$emb" = "ok" ]; check "embedder reachable (semantic mode)" $?

# 2. Write
w=$(curl -sf -X POST "$URL/api/notes" -H 'Content-Type: application/json' \
	-d '{"title":"Smoke Test Note","body":"engram smoke test: pg_dump WAL archiving [[Smoke Link Target]]","tags":["smoke","source:agent"]}')
check "write note" $?
id=$(echo "$w" | json "['id']")
[ "$id" = "smoke-test-note" ]; check "slug id ($id)" $?

# 3. Read back
r=$(curl -sf "$URL/api/notes/$id")
check "read note" $?
title=$(echo "$r" | json "['title']")
[ "$title" = "Smoke Test Note" ]; check "roundtrip title" $?

# 4. Semantic search (falls back to keyword if embedder is down)
s=$(curl -sf "$URL/api/search?q=postgres+backup+archiving&limit=3")
check "semantic search request" $?
kind=$(echo "$s" | json "[0]['kind']")
found=$(echo "$s" | python3 -c "import sys,json;hits=json.load(sys.stdin);print(0 if any(h['note']['id']=='smoke-test-note' for h in hits) else 1)" 2>/dev/null)
check "search finds the note (kind=$kind)" "${found:-1}"

# 5. Keyword search explicitly
k=$(curl -sf "$URL/api/search?q=pg_dump&kind=keyword")
found=$(echo "$k" | python3 -c "import sys,json;hits=json.load(sys.stdin);print(0 if any(h['note']['id']=='smoke-test-note' for h in hits) else 1)" 2>/dev/null)
check "keyword search" "${found:-1}"

# 6. Backlinks (wikilink parsed at write time)
curl -sf -X POST "$URL/api/notes" -H 'Content-Type: application/json' \
	-d '{"title":"Smoke Link Target","body":"target of the smoke link"}' > /dev/null
b=$(curl -sf "$URL/api/notes/smoke-link-target/links")
found=$(echo "$b" | python3 -c "import sys,json;bl=json.load(sys.stdin);print(0 if any(x['id']=='smoke-test-note' for x in bl) else 1)" 2>/dev/null)
check "backlinks" "${found:-1}"

# 7. Reembed (missing-only backfill; 0 reembedded is fine when healthy)
re=$(curl -sf -X POST "$URL/api/reembed")
check "reembed endpoint" $?
echo "      $re"

# 8. Cleanup
curl -sf -X DELETE "$URL/api/notes/$id" > /dev/null;               check "delete note" $?
curl -sf -X DELETE "$URL/api/notes/smoke-link-target" > /dev/null; check "delete link target" $?

# 9. Final health
h=$(curl -sf "$URL/api/health")
check "final health" $?
echo "      $h"

echo "== $pass passed, $fail failed =="
[ "$fail" -eq 0 ]
