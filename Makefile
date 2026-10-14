# Engram build targets.
# Usage: make <target>

.PHONY: build test lint fmt vet assets

VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o engram ./cmd/engram

test:
	go test -race -cover ./...

integration:
	go test -tags=integration -race ./internal/store/...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .
	goimports -w -local github.com/davasorus/engram .

vet:
	go vet ./...

assets:
	npm ci && npm run assets

drift:
	sh scripts/check-asset-drift.sh
