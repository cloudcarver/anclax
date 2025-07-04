# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Overview

Anchor is a framework for building serverless and reliable applications with Go. It provides authentication & authorization with Macaroons, asynchronous task management with at-least-once delivery, database query interface with sqlc, HTTP API server with Fiber, and a plugin system.

## Development Commands

### Core Commands
- `anchor generate` - Generate code from YAML specifications (API, tasks, database queries)
- `anchor init .` - Initialize a new Anchor project
- `make dev` - Start development environment with Docker Compose
- `make reload` - Restart development containers
- `make db` - Connect to PostgreSQL database
- `make test` - Run integration tests (requires Python test setup)
- `make prepare-test` - Set up Python test environment
- `make ut` - Run unit tests with coverage report
- `make gen` - Copy templates from examples/simple to cmd/anchor/initFiles

### Web Development (Next.js frontend)
- `cd web && npm run dev` - Start Next.js development server
- `cd web && npm run build` - Build production Next.js app
- `cd web && npm run gen` - Generate TypeScript client from OpenAPI spec

## Code Generation Architecture

Anchor uses a sophisticated code generation system driven by YAML specifications:

### Key Configuration Files
- `anchor.yaml` - Main configuration defining external tools, code generation paths, and mock generation
- `api/v1.yaml` - OpenAPI 3.0 specification for HTTP APIs
- `api/tasks.yaml` - Task definitions for async job processing
- `dev/sqlc.yaml` - Database query generation configuration

### Generated Code Structure
- `pkg/zgen/apigen/` - Generated API handlers and types from OpenAPI spec
- `pkg/zgen/querier/` - Generated database query interfaces from SQL files
- `pkg/zgen/taskgen/` - Generated task runners and interfaces
- `pkg/zcore/model/` - Generated mocks for database models

### Code Generation Flow
1. Define schemas in YAML files (`api/v1.yaml`, `api/tasks.yaml`)
2. Write SQL queries in `sql/queries/` directory
3. Run `anchor generate` to generate Go interfaces and types
4. Implement the generated interfaces in your application code
5. Use dependency injection (Wire) to wire components together

## Application Architecture

### Core Components
- **Application** (`pkg/app/app.go`) - Main application orchestrator managing server, worker, auth, and metrics
- **Server** (`pkg/server/server.go`) - HTTP server using Fiber framework with middleware stack
- **Authentication** (`pkg/auth/auth.go`) - Macaroon-based authentication with caveats for authorization
- **Task Management** (`pkg/taskcore/`) - Async task processing with at-least-once delivery guarantees
- **Database Layer** (`pkg/zcore/model/`) - Database abstraction with generated queries via sqlc

### Architecture Patterns
- **Dependency Injection**: Uses Google Wire for compile-time dependency injection
- **Interface-Driven Design**: All major components implement interfaces for testability
- **Generated Code**: Heavy use of code generation to maintain type safety between YAML specs and Go code
- **Event-Driven**: Task system processes events asynchronously with cron job support

### Key Directories
- `cmd/anchor/` - CLI tool for project initialization and code generation
- `pkg/` - Core framework packages
- `examples/simple/` - Example application showing framework usage
- `web/` - Next.js frontend with generated TypeScript client
- `sql/` - Database migrations and query definitions
- `api/` - OpenAPI specifications and task definitions

## Database Management

- Uses PostgreSQL with migrations in `sql/migrations/`
- Queries defined in `sql/queries/` and generated via sqlc
- Database models support nullable types with pointer semantics
- JSON columns use Go's `json.RawMessage` for flexible data storage

## Authentication & Authorization

- Macaroon-based tokens with caveat system for fine-grained permissions
- Support for access tokens (10min default) and refresh tokens (2hr default)
- User context and refresh-only caveats for token validation
- Fiber middleware for request authentication

## Testing

- Unit tests tagged with `ut` build constraint
- Integration tests using Python client generated from OpenAPI spec
- Mock generation for all major interfaces using `go:generate` and mockgen
- Coverage reports generated in HTML format