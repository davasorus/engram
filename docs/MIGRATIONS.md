# Database migrations

Liquibase owns engram's schema, not the Go binary. The engram process never
creates or alters tables. On startup, it only checks that the schema is
present (`checkSchema` in `internal/store/postgres.go`, which verifies the
`notes` table and the columns this build depends on). It fails fast with an
actionable message if anything is missing. This design keeps engram a pure,
distroless Go binary, while giving migrations a real, versioned, tracked
home: Liquibase's `databasechangelog` table.

## Single source of truth

The schema is defined in exactly one place:

- `db/changelog/changelog-master.yaml` — the master changelog. It includes
  each change file in order.
- `db/changelog/changes/*.yaml` — the Liquibase YAML changesets.

That directory is baked into a migration image
(`db/Dockerfile`: `FROM docker.io/liquibase/liquibase:4.29` plus
`COPY db/changelog/`). Every deployment path uses this one image, so the
changelog is never duplicated or hand-copied anywhere. There are no bind
mounts and no ConfigMaps that hold SQL.

## Changeset conventions

Changesets use Liquibase's database-agnostic YAML tags (`createTable`,
`createIndex`, `addColumn`) wherever possible. Some Postgres-specific DDL
has no YAML abstraction: the `vector` extension, the `vector(768)` column
type, and the HNSW and GIN index types. These changesets use raw `sql:`
blocks with `IF NOT EXISTS`.

YAML tags like `createTable` are not idempotent, unlike
`CREATE TABLE IF NOT EXISTS`. For this reason, each such changeset carries a
precondition, so it stays safe against a database that already has the
object:

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

`onFail: MARK_RAN` means: if the object already exists, record this
changeset as applied and move on. This setting lets Liquibase adopt an
existing database cleanly, instead of raising an error.

The embedding width is hardcoded as `vector(768)` in changeset
`002-notes-table`. It is not parameterized. The embed model fixes the
dimension (nomic-embed-text-v1.5 = 768), and the dimension changes only
when you swap models. A model swap requires re-embedding every note in any
case. `ENGRAM_DIMS`, the app-side setting, must match this value.
`checkSchema` verifies the column exists at startup.

## Migrations run automatically, everywhere

| Path | How migrations run |
|------|--------------------|
| **compose** (prod) | a `migrate` service (the migration image) runs `liquibase update`, then exits. |
| **compose dev** (`Compose.dev.yml`) | same, but builds the migration image from source locally instead of pulling. |
| **kube** | an `initContainer` named `migrate` on the engram Deployment runs `liquibase update` before the engram container starts. |
| **integration tests** | apply the schema directly (`applySchema` in `postgres_integration_test.go`); the tests exercise the store's query paths without the Liquibase JVM. |
| **local `go run` against a bare DB** | not automatic, by design. engram fails fast and tells you to run migrations. Use compose, or run the migration image by hand (see below). |

Liquibase applies only changesets not already recorded in
`databasechangelog`; preconditions skip changesets whose objects already
exist. This behavior is idempotent, so the migrate step runs safely on
every startup and applies whatever is new.

### A caveat on `podman-compose` ordering

The compose file expresses `engram -> depends_on -> migrate:
service_completed_successfully`. Real Kubernetes, through the
initContainer, and Docker Compose honor this rule and block engram until
migrations succeed. **`podman-compose` 1.0.6 does not honor it** — it uses
Podman's `--requires`, which ensures only that the dependency *started*,
not that it exited successfully. Under `podman-compose`, engram can start
even when `migrate` fails.

`checkSchema` is therefore the real gate. If migrations did not fully
apply, engram refuses to start and reports a clear error, instead of
serving requests against a broken schema. This gate is more robust than
trusting the compose runtime's dependency handling.

## Run migrations by hand (local dev)

```bash
podman build -f db/Dockerfile -t engram-migrate:local .
podman run --rm --network host engram-migrate:local \
  --url=jdbc:postgresql://localhost:55432/engram \
  --username=engram --password='P@ssw0rd' \
  --changelog-file=changelog/changelog-master.yaml \
  update
```

Note the explicit `--changelog-file` flag; the image does not rely on a
defaults file. There is no `--changelog-parameters` flag, because the
dimension is hardcoded in the changeset, not passed in.

## Add a migration

1. Add a YAML changeset file under `db/changelog/changes/`, for example
   `003-your-change.yaml`. Add a precondition on any non-idempotent tag:

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

2. Reference the new file from `changelog-master.yaml`; add an `include`
   entry.
3. If the code depends on the new column, add the column to the `required`
   list in `checkSchema` (`internal/store/postgres.go`), so a missing
   migration fails fast. Handle NULLs in the store's `SELECT` statements,
   for example with `COALESCE`.
4. Cut a release (see below), so the release process rebuilds and
   republishes the migration image.
5. Redeploy. Liquibase applies only the new changesets.

Data migrations, such as backfills and renames, are changesets too. The
project deliberately avoids automatic data backfills. For example, the
`project` column (added in `002-add-project.yaml`) is nullable, so
existing rows stay NULL (unscoped) until you choose to set them. Scope
notes individually in the UI, or write a targeted data migration or
`UPDATE` statement for the specific rows you mean. Never write a blanket
update.

## Publishing

The release workflow (`.github/workflows/release.yml`) builds and pushes
the migration image on a version tag:
`ghcr.io/davasorus/engram-migrate:<version>` and `:latest`, tagged to
match the engram image (both `X.Y.Z`, with no leading `v`).

## Adopt an existing database

A database created by engram's old in-process migrator already has the
base schema. On the first `liquibase update`, the preconditions
(`MARK_RAN`) and the `IF NOT EXISTS` clauses on the raw-SQL changesets
treat the already-present objects as no-ops, while Liquibase records the
changesets as applied. Genuinely new, additive changesets, like
`002-add-project`, then run normally. If Liquibase objects to a checksum
on a hand-touched database, run `liquibase changelog-sync` once to
baseline it without executing anything.
