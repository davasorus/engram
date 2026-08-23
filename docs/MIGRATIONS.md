# Database migrations

engram's schema is owned by **Liquibase**, not the Go binary. The engram
process never creates or alters tables — on startup it only *checks* that the
schema is present (`checkSchema` in `internal/store/postgres.go`, which verifies
the `notes` table **and** the columns this build depends on) and fails fast with
an actionable message if anything is missing. This keeps engram a pure,
distroless Go binary while giving migrations a real, versioned, tracked home
(Liquibase's `databasechangelog` table).

## Single source of truth

The schema is defined in exactly one place:

- `db/changelog/changelog-master.yaml` — the master changelog (includes each
  change file in order).
- `db/changelog/changes/*.yaml` — Liquibase **YAML** changesets.

That directory is baked into a **migration image** (`db/Dockerfile`:
`FROM docker.io/liquibase/liquibase:4.29` + `COPY db/changelog/`). Every
deployment path uses that one image, so the changelog is never duplicated or
hand-copied anywhere (no bind mounts, no ConfigMaps holding SQL).

## Changeset conventions

Changesets use Liquibase's database-agnostic YAML tags (`createTable`,
`createIndex`, `addColumn`) wherever possible. Postgres-specific DDL that has no
YAML abstraction — the `vector` extension, the `vector(768)` column type, and
the HNSW / GIN index types — uses raw `sql:` blocks with `IF NOT EXISTS`.

Because YAML tags like `createTable` are **not** idempotent (unlike
`CREATE TABLE IF NOT EXISTS`), each such changeset carries a **precondition** so
it is safe against a database that already has the object:

```yaml
- changeSet:
    id: 002-notes-table
    author: engram
    preConditions:
      - onFail: MARK_RAN
      - not:
          - tableExists:
              tableName: notes
    changes:
      - createTable: { ... }
```

`onFail: MARK_RAN` means "if the object already exists, record this changeset as
applied and move on" — so an existing database is adopted cleanly rather than
erroring.

The embedding width is **hardcoded** as `vector(768)` in changeset
`002-notes-table`. It is not parameterized: the dimension is fixed by the embed
model (nomic-embed-text-v1.5 = 768) and only changes when you swap models, which
requires re-embedding every note anyway. `ENGRAM_DIMS` (the app-side setting)
must match this value; `checkSchema` verifies the column exists at startup.

## It runs automatically, everywhere

| Path | How migrations run |
|------|--------------------|
| **compose** (prod) | a `migrate` service (the migration image) runs `liquibase update`, then exits. |
| **compose dev** (`Compose.dev.yml`) | same, but builds the migration image from source locally instead of pulling. |
| **kube** | an `initContainer` named `migrate` on the engram Deployment runs `liquibase update` before the engram container starts. |
| **integration tests** | apply the schema directly (`applySchema` in `postgres_integration_test.go`) — the store's query paths are tested without needing the Liquibase JVM. |
| **local `go run` against a bare DB** | not automatic by design — engram fails fast telling you to run migrations. Use compose, or run the migration image by hand (below). |

Liquibase is **idempotent**: it applies only changesets not already recorded in
`databasechangelog` (and preconditions skip ones whose objects already exist),
so the migrate step runs safely on *every* startup and applies whatever is new.

### A caveat on `podman-compose` ordering

The compose file expresses `engram -> depends_on -> migrate:
service_completed_successfully`. Real Kubernetes (via the initContainer) and
Docker Compose honor this and block engram until migrations succeed.
**`podman-compose` 1.0.6 does not** — it uses podman's `--requires`, which only
ensures the dependency *started*, not that it exited successfully. So under
`podman-compose`, engram can start even if `migrate` failed.

engram's `checkSchema` is therefore the real gate: if migrations did not fully
apply, engram refuses to start with a clear error instead of serving against a
broken schema. This is more robust than trusting the compose runtime's
dependency handling.

## Run migrations by hand (local dev)

```bash
podman build -f db/Dockerfile -t engram-migrate:local .
podman run --rm --network host engram-migrate:local \
  --url=jdbc:postgresql://localhost:55432/engram \
  --username=engram --password='P@ssw0rd' \
  --changelog-file=changelog/changelog-master.yaml \
  update
```

Note the explicit `--changelog-file` (the image does not rely on a defaults
file) and that there is **no** `--changelog-parameters` — the dimension is
hardcoded in the changeset, not passed in.

## Adding a migration

1. Add a YAML changeset file under `db/changelog/changes/`, e.g.
   `003-your-change.yaml`. Use a precondition on any non-idempotent tag:

   ```yaml
   databaseChangeLog:
     - changeSet:
         id: 020-add-foo
         author: engram
         preConditions:
           - onFail: MARK_RAN
           - not:
               - columnExists:
                   tableName: notes
                   columnName: foo
         changes:
           - addColumn:
               tableName: notes
               columns:
                 - column: { name: foo, type: TEXT }
         rollback:
           - dropColumn: { tableName: notes, columnName: foo }
   ```

2. Reference it from `changelog-master.yaml` (add an `include`).
3. If the code depends on the new column, add it to the `required` list in
   `checkSchema` (`internal/store/postgres.go`) so a missing migration fails
   fast, and handle NULLs in the store's `SELECT`s (e.g. `COALESCE`).
4. Cut a release (see below) so the migration image is rebuilt and republished.
5. Redeploy. Liquibase applies only the new changesets.

**Data migrations** (backfills, renames) are just changesets too. There is
deliberately **no automatic data backfill** — for example, the `project` column
(added in `002-add-project.yaml`) is **nullable**, so existing rows stay NULL
(unscoped) until you choose to set them. Scope notes individually in the UI, or
write a targeted data migration/`UPDATE` for the specific rows you mean — never
a blanket update.

## Publishing

The release workflow (`.github/workflows/release.yml`) builds and pushes the
migration image on a version tag:
`ghcr.io/davasorus/engram-migrate:<version>` and `:latest`, tagged to match the
engram image (both `X.Y.Z`, no leading `v`).

## Adopting an existing database

A database created by engram's old in-process migrator already has the base
schema. On the first `liquibase update`, the preconditions (`MARK_RAN`) and the
`IF NOT EXISTS` on the raw-SQL changesets make the already-present objects
no-ops, while Liquibase records the changesets as applied. Genuinely new,
additive changesets (like `002-add-project`) then run normally. If Liquibase
ever objects to a checksum on a hand-touched database, run `liquibase
changelog-sync` once to baseline it without executing.