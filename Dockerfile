# Multi-stage build → a small image with a single static binary.
# Pure-Go SQLite (modernc) means CGO_ENABLED=0 works, so the final image can be
# distroless/static with no libc.

FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static, stripped binary. Templates/static assets are embedded via //go:embed,
# so the binary is self-contained.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/engram ./cmd/engram

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/engram /engram
# /data holds the SQLite database (mount a volume here to persist).
VOLUME ["/data"]
EXPOSE 8088
ENTRYPOINT ["/engram"]
