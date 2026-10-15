// Package main provides the deploy hint text shown when the database is
// unreachable. It lives in its own file so main.go stays focused on startup
// flow and the constant can be tested independently.
package main

// deployHint is printed when the database is unreachable — the most common
// state for someone who ran the bare image with no configuration. It has to
// carry them to a working deployment on its own, because a `podman run` user
// has no compose file, no .env, and no repo checked out.
const deployHint = `
engram needs a pgvector-enabled Postgres. This image does not bundle one.

  Required env (one of):
    ENGRAM_DSN           full postgres:// connection string, or
    ENGRAM_DB_HOST/PORT/USER/PASSWORD/NAME   discrete pieces (password can
                         then come from a real secret, not a string in git)
  Plus:
    ENGRAM_EMBED_URL     OpenAI-compatible embeddings endpoint (e.g. LM Studio).
                         Accepts a comma-separated list for fallback.

  Kube deploy, no clone (secret is generated locally, never committed;
  all tunables live in the ConfigMap at the top of the manifest):
    curl -LO https://github.com/davasorus/engram/releases/latest/download/engram-kube.yaml
    curl -LO https://github.com/davasorus/engram/releases/latest/download/secret.example.yaml
    sed "s/REPLACE_ME/$(openssl rand -hex 20)/" secret.example.yaml | podman kube play -
    $EDITOR engram-kube.yaml    # ConfigMap: embed URL/port, dims, db names
    podman kube play engram-kube.yaml

  Or compose (fetch the two files, edit .env, up):
    curl -LO  https://github.com/davasorus/engram/releases/latest/download/compose.yml
    curl -Lo .env https://github.com/davasorus/engram/releases/latest/download/env.example
    podman-compose up -d

  Docs: https://github.com/davasorus/engram
`
