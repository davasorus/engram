# Upgrading engram

engram ships as two versioned, multi-arch images published together on each
release tag:

- `ghcr.io/davasorus/engram:<version>` — the service.
- `ghcr.io/davasorus/engram-migrate:<version>` — Liquibase + the schema
  changelog (see [MIGRATIONS.md](MIGRATIONS.md)).

Both are tagged `X.Y.Z` (no leading `v`) and are meant to be deployed as a
matched pair, so the schema version always matches the app version.

## Production upgrade (compose, pulling published images)

Production **pulls** images — it never builds. The base `compose/compose.yml`
has no build stanzas; it references the images by `ENGRAM_VERSION`.

```bash
cd compose

# 1. point at the new version
$EDITOR .env            # set ENGRAM_VERSION=X.Y.Z  (pin it; don't ride :latest)

# 2. pull the new engram + engram-migrate images
podman-compose -f compose.yml pull

# 3. recreate the stack
podman-compose -f compose.yml down
podman-compose -f compose.yml up -d
```

On `up`, the `migrate` service runs `liquibase update` (applying any new
changesets to your existing database — your data in the `pgdata` volume is
preserved), then engram starts and verifies the schema.

### Why `down` before `up` is required

`podman-compose` 1.0.6, when a container of the same name already exists, does
**not** recreate it from the new image — it silently `podman start`s the *old*
container, so your upgrade appears to succeed while still running the previous
version. Always `down` first (or `podman rm -f engram engram-migrate engram-db`)
so `up` creates fresh containers from the pulled images.

`down` removes containers but keeps the `pgdata` volume, so notes and schema
survive the upgrade.

### Verify the upgrade took

```bash
podman inspect engram --format '{{.Image}}'      # should be the new image id
curl -s localhost:8088/api/health                # {"status":"ok",...}
podman logs engram-migrate                        # Liquibase "Update successful"
```

## Development upgrade (build from source)

When working on engram itself, the dev overlay builds both images locally
instead of pulling. Note the capital-C filename: `Compose.dev.yml`.

```bash
cd /path/to/engram

# rebuild both images from source
podman build -f Dockerfile    -t engram:local         .
podman build -f db/Dockerfile -t engram-migrate:local .

# recreate (down first — same podman-compose caveat as above)
podman-compose -f compose/compose.yml -f compose/Compose.dev.yml down
podman-compose -f compose/compose.yml -f compose/Compose.dev.yml up -d
```

If you changed baked-in assets (templates, static files, the changelog), add
`--no-cache` to the relevant `podman build` so a cached layer doesn't serve
stale content.

> `podman-compose` 1.0.6 has a build-context bug with per-service
> `build: { context: .., dockerfile: X }` (`Dockerfile not found in ..`).
> Build the images manually as above, then `up` **without** `--build`.

## Kubernetes upgrade

Bump the image tags in `kube/engram.yaml` (both the engram container and the
`migrate` initContainer) to the new version and re-apply:

```bash
kubectl apply -f kube/engram.yaml     # real cluster
# or: podman play kube kube/engram.yaml
```

The `migrate` initContainer runs `liquibase update` before engram starts on
every rollout, so schema changes apply automatically. Unlike `podman-compose`,
Kubernetes honors init-container ordering — engram will not start until
migrations succeed.

## Rolling back

- **App only:** point `ENGRAM_VERSION` back to the previous tag and repeat the
  compose upgrade steps. Safe as long as the older app is compatible with the
  current schema.
- **Schema:** Liquibase changesets carry `rollback` blocks. To undo the last
  migration, run the migration image with `rollbackCount 1` (or
  `rollback <tag>`) instead of `update`. Roll the app back **first** if the
  older app can't work without the newer schema — and remember additive,
  nullable changes (the project column) generally don't require a schema
  rollback when reverting the app.

## Backups

The `pgdata` Postgres volume is the unit of backup. Before a significant
upgrade:

```bash
podman exec engram-db pg_dump -U engram -p 55432 engram > engram-backup.sql
```