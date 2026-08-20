# Multi-stage build → a small image with a single static binary.
# CGO is disabled (pure-Go deps), so the final image is distroless/static.
# Image names are fully qualified so they resolve regardless of a host's
# unqualified-search-registries configuration.

FROM docker.io/library/golang:1.25-alpine AS build
WORKDIR /src
# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static, stripped binary. Templates/static assets are embedded via //go:embed,
# so the binary is self-contained. Version/commit stamped for -ldflags.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/engram ./cmd/engram

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/engram /engram
EXPOSE 8088
ENTRYPOINT ["/engram"]
