# Project Overview
engram is a memory service for AI agents. It stores markdown notes as data in a database. The system uses Postgres and pgvector for storage. It provides semantic search and link analysis. It offers an MCP interface for agents. It also provides a REST API and a web UI for humans.

# Repository Structure
- `cmd/engram`: This directory contains the main entry point.
- `internal/core`: This package contains the domain model and core interfaces.
- `internal/store`: This package implements storage logic using Postgres.
- `internal/embed`: This package handles embedding generation.
- `internal/mcp`: This package provides the Model Context Protocol interface.
- `internal/rest`: This package provides the REST API.
- `internal/web`: This package serves the web user interface.
- `compose/`: This directory contains Docker Compose files.
- `kube/`: This directory contains Kubernetes configuration files.

# Build and Test
Run `go test ./...` to run unit tests.
Run `go test -tags=integration ./internal/store/...` for integration tests.
Run `golangci-lint run` to check code quality.
Run `gofmt -l .` to verify code formatting.

# Code Conventions
Follow standard Go coding styles.
Use the core interfaces in `internal/core`.
Keep internal logic within the `internal` directory.
Ensure consistency between the REST and MCP implementations.
Match all data structures with the definitions in `inner/core`.

# Commit Messages
Use Conventional Commits for all changes.
Follow the format "type(scope): description".
Use types: feat, fix, docs, style, refactor, perf, test, build, ci, chore.
Use imperative mood for descriptions.
Keep the subject under 72 characters.
Add a body for complex changes.
Use an exclamation point after type/scope to mark breaking changes.

# Documentation Language
Write all documentation in ASD-STE100 Simplified Technical English.
Use active voice.
Use simple verb tenses.
Write one instruction per sentence.
Keep sentences under 25 words.
Use one term for one thing.
Do not use vague words.
Do not use -ing forms.
Keep paragraphs to one topic.
Limit each paragraph to six sentences.

# Memory Management
Start every note name with the repository name.
Add the repository name as a tag for each note.
