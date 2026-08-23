# engram database migrations — Liquibase + the changelog, baked into one image.
#
# This is the SINGLE SOURCE OF TRUTH for the schema at deploy time: both
# compose (the `migrate` service) and kube (the `migrate` initContainer)
# reference this image, so the changelog is never duplicated or hand-copied
# into a ConfigMap. Built and published alongside the engram image.
#
#   Build:  podman build -f db/Dockerfile -t ghcr.io/davasorus/engram-migrate:dev .
#   Run:    podman run --rm --network host ghcr.io/davasorus/engram-migrate:dev \
#             --url=jdbc:postgresql://localhost:5432/engram \
#             --username=engram --password=... \
#             --changelog-parameters=dims:768 update
FROM docker.io/liquibase/liquibase:4.29

# The changelog lives at the Liquibase default search path (/liquibase/changelog).
# Callers pass --changelog-file=changelog/changelog-master.yaml along with the
# connection flags (see compose/compose.yml and kube/engram.yaml).
COPY db/changelog/ /liquibase/changelog/
