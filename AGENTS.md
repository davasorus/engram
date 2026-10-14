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
- `internal/complete`: This package provides the completion (summarization) client.
- `internal/cliclient`: This package provides the CLI REST client for the agent.
- `compose/`: This directory contains Docker Compose files.
- `kube/`: This directory contains Kubernetes configuration files.

# Build and Test
See [README.md](README.md) for build and test commands.

# Code Conventions
See [README.md](README.md) for code conventions.

# Commit Messages
See [README.md](README.md) for commit message conventions.
