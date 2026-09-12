# Multi-stage build → a small image with a single static binary.
# CGO is disabled (pure-Go deps), so the final image is distroless/static.
# Image names are fully qualified so they resolve regardless of a host's
# unqualified-search-registries configuration.

FROM docker.io/library/golang:1.27-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build
WORKDIR /src
# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static, stripped binary. Templates/static assets are embedded via //go:embed,
# so the binary is self-contained. Version/commit stamped for -ldflags.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/engram ./cmd/engram

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/engram /engram
EXPOSE 8088
# Exec-form HEALTHCHECK: runs the binary directly, no shell involved. This
# image has none (distroless/static), and CMD-SHELL-style healthchecks set
# at the compose/orchestrator level can never work here for that reason.
# podman-compose also mistranslates exec-form `test:` arrays in compose.yml
# healthchecks, garbling them into a broken `/bin/sh -c '...'` invocation —
# baking the check into the image sidesteps both problems.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD ["/engram", "-healthcheck", "-addr", ":8088"]
ENTRYPOINT ["/engram"]
