---
name: anclax-develop-feature
description: Develop, review, or refactor Go services built with Anclax, including OpenAPI specs, HTTP handlers, websocket/realtime features, service/business logic, database/sqlc changes, async tasks, and Wire dependency injection.
---

# Anclax Development Workflow

Use Anclax generated types as the contract between layers and keep specs/SQL as the source of truth.

## Design principles

- Singleton services with dependency injection.
- High cohesion, low coupling.

## Core flow

1. Inspect `anclax.yaml` to learn generation paths and enabled generators.
2. Update sources first:
   - OpenAPI: the matching `oapi-codegen` entry `path` (commonly `api/openapi`, which may be a single file or a directory of merged fragments)
   - Tasks: the matching `task-handler` entry `path` (commonly `api/tasks/tasks.yaml`)
   - DB schema: `sql/migrations`
   - Queries: `sql/queries`
3. Run `anclax gen` after any spec/SQL/Wire changes.
4. If you modify `examples/`, run `make gen` to refresh `cmd/anclax/initFiles` and normalize template-specific `go.mod` content.
5. Implement code against generated interfaces and types.
6. Add unit tests for service logic with mocks.

## Layering rules

- HTTP handler: parse HTTP, call service, map errors to HTTP responses.
- Websocket handler: parse frames, manage session state, call service logic, and write websocket responses.
- Service: implement business logic, topic/subscription policy, and event publication.
- Model: use `pkg/zcore/model` and sqlc-generated queries.
- Async tasks: define in the task spec configured under `task-handler` (commonly `api/tasks/tasks.yaml`), implement `taskgen.ExecutorInterface`, enqueue via `taskgen.TaskRunner`.

## References and Examples
  - [Config](./anclax-config.md): How to use `anclax.yaml` for generator inputs/outputs.
  - [CRUD operations](./crud.md): End-to-end CRUD flow and mapping examples.
  - [OpenAPI Spec](./openapi-spec.md): Conventions for OpenAPI specs in Anclax.
  - [Business Logic](./business-logic.md): Service-layer rules and error handling.
  - [Authentication](./authentication.md): Simple auth config, macaroon tokens, and custom auth API patterns.
  - [Database](./database.md): SQL/schema rules and transaction helpers.
  - [Websocket / Realtime](./websocket.md): `lib/ws` handlers, hubs, session lifecycle, subscriptions, and broadcasts.
  - [Dependency Injection](./dependency-injestion.md): Wire DI conventions.
  - [Async Tasks](./async-tasks.md): Task definitions, execution, retries, and hooks.
  - [Example template generation](./template-generation.md): Updating `examples/` and regenerating `cmd/anclax/initFiles`, including `go.mod` normalization from `VERSION`.
  - [Multi-service repos](./multi-service-repos.md): Organizing multiple apps under `app/` with shared or service-specific modules.
