# Multi-stage build → a small image with a single static binary.
# CGO is disabled (pure-Go deps), so the final image is distroless/static.
# Image names are fully qualified so they resolve regardless of a host's
# unqualified-search-registries configuration.

FROM docker.io/library/golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build
WORKDIR /src
# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static, stripped binary. Templates/static assets are embedded via //go:embed,
# so the binary is self-contained. Version/commit stamped for -ldflags.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/engram ./cmd/engram

FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a
COPY --from=build /out/engram /engram
EXPOSE 8088
ENTRYPOINT ["/engram"]
