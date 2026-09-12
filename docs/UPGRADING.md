# Upgrading engram

engram ships as two versioned, multi-arch images, published together on
each release tag:

- `ghcr.io/davasorus/engram:<version>` — the service.
- `ghcr.io/davasorus/engram-migrate:<version>` — Liquibase and the schema
  changelog (see [MIGRATIONS.md](MIGRATIONS.md)).

Both images carry the tag `X.Y.Z` (no leading `v`). Deploy them as a
matched pair, so the schema version always matches the app version.

## Production upgrade (compose, pulling published images)

Production pulls images; it never builds them. The base
`compose/compose.yml` file has no build stanzas. It references the images
by `ENGRAM_VERSION`.

```bash
cd compose

# 1. Point at the new version.
$EDITOR .env            # set ENGRAM_VERSION=X.Y.Z  (pin it; don't ride :latest)

# 2. Pull the new engram and engram-migrate images.
podman-compose -f compose.yml pull

# 3. Recreate the stack.
podman-compose -f compose.yml down
podman-compose -f compose.yml up -d
```

On `up`, the `migrate` service runs `liquibase update`. This step applies
any new changesets to your existing database; your data in the `pgdata`
volume stays intact. Then engram starts and verifies the schema.

### Why you must run `down` before `up`

When a container of the same name already exists, `podman-compose` 1.0.6
does not recreate it from the new image. Instead, it silently runs
`podman start` on the old container, so the upgrade appears to succeed
while the previous version keeps running. Always run `down` first (or run
`podman rm -f engram engram-migrate engram-db`), so `up` creates fresh
containers from the pulled images.

`down` removes containers but keeps the `pgdata` volume, so notes and
schema survive the upgrade.

### Verify the upgrade

```bash
podman inspect engram --format '{{.Image}}'      # should be the new image id
curl -s localhost:8088/api/health                # {"status":"ok",...}
podman logs engram-migrate                        # Liquibase "Update successful"
```

## Development upgrade (build from source)

When you work on engram itself, use the dev overlay to build both images
locally instead of pulling them. Note the capital-C filename:
`Compose.dev.yml`.

```bash
cd /path/to/engram

# Rebuild both images from source.
podman build -f Dockerfile    -t engram:local         .
podman build -f db/Dockerfile -t engram-migrate:local .

# Recreate the stack (run down first; the same podman-compose caveat applies).
podman-compose -f compose/compose.yml -f compose/Compose.dev.yml down
podman-compose -f compose/compose.yml -f compose/Compose.dev.yml up -d
```

If you changed baked-in assets, such as templates, static files, or the
changelog, add `--no-cache` to the relevant `podman build` command. This
flag prevents a cached layer from serving stale content.

`podman-compose` 1.0.6 has a build-context bug with per-service
`build: { context: .., dockerfile: X }` settings
(`Dockerfile not found in ..`). Build the images manually, as shown above,
then run `up` without the `--build` flag.

## Kubernetes upgrade

Bump the image tags in `kube/engram.yaml`, for both the engram container
and the `migrate` initContainer, to the new version. Then re-apply the
manifest:

```bash
kubectl apply -f kube/engram.yaml     # real cluster
# or: podman play kube kube/engram.yaml
```

The `migrate` initContainer runs `liquibase update` before engram starts
on every rollout, so schema changes apply automatically. Unlike
`podman-compose`, Kubernetes honors init-container ordering: engram does
not start until migrations succeed.

## Rolling back

- **App only:** point `ENGRAM_VERSION` back to the previous tag and repeat
  the compose upgrade steps. This rollback is safe as long as the older
  app works with the current schema.
- **Schema:** Liquibase changesets carry `rollback` blocks. To undo the
  last migration, run the migration image with `rollbackCount 1` (or
  `rollback <tag>`) instead of `update`. Roll the app back first, if the
  older app cannot work without the newer schema. Note that additive,
  nullable changes, such as the project column, generally do not need a
  schema rollback when you revert the app.

## Backups

The `pgdata` Postgres volume is the unit of backup. Before a significant
upgrade, run:

```bash
podman exec engram-db pg_dump -U engram -p 55432 engram > engram-backup.sql
```
