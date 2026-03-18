# Multi-service Repositories

Use this guidance for service-oriented Anclax repos with multiple app entrypoints. In practice this is often a modular monolith or a multi-entrypoint repo, not necessarily independently deployed microservices.

## Repository layout

Use these conventions:

- `app/<service>/app.go`: service bootstrap and startup logic.
- `app/<service>/injection.go`: inject Anclax-managed dependencies such as auth, task store, or other framework modules.
- `app/<service>/wire/`: Wire setup for that service.
- `pkg/`: reusable modules shared across services.

A repo may also keep a single default app directly under `app/` and grow into `app/service_a`, `app/service_b`, ... later.

## Generator config

Treat `anclax.yaml` as the source of truth. `oapi-codegen`, `task-handler`, `sqlc`, and `wire` can each have multiple entries.

For each service:
1. Add or update the matching generator entries.
2. Keep output paths and package names distinct to avoid collisions.
3. Reuse the shared `schemas` config when multiple services reference the same schema files.

Typical pattern:

```yaml
schemas:
  path: api/schemas
  output: pkg/zgen/schemas

oapi-codegen:
  - path: app/service_a/api/v1.yaml
    out: app/service_a/zgen/apigen/spec_gen.go
    package: serviceaapigen
  - path: app/service_b/api/v1.yaml
    out: app/service_b/zgen/apigen/spec_gen.go
    package: servicebapigen

task-handler:
  - path: app/service_a/api/tasks.yaml
    out: app/service_a/zgen/taskgen/runner_gen.go
    package: serviceataskgen

wire:
  - path: ./app/service_a/wire
  - path: ./app/service_b/wire

sqlc:
  - path: sql/sqlc.yaml
  - path: app/service_b/sql/sqlc.yaml
```

## Shared vs service-specific data layer

Default to a shared model and shared SQL layer when services operate on the same database and tables:

- shared SQL: `sql/`
- shared model: `pkg/model`

Give a service its own data layer only when it needs isolated queries, migrations, or ownership boundaries:

- service SQL: `app/<service>/sql`
- service model: `app/<service>/model`

If a service owns its own migrations, set a unique migration table name in that model package. Do not reuse the same migration table name across different migration sets.

## API, handlers, and tasks

A service may keep its own:
- OpenAPI spec
- task spec
- handlers
- async task executor
- Wire graph

Place service-specific code near the service when that improves ownership and clarity, for example:

- `app/<service>/api/...`
- `app/<service>/handler/...`
- `app/<service>/asynctask/...`

Keep reusable business modules in `pkg/`.

## Implementation workflow

1. Inspect `anclax.yaml` and identify which generator entries belong to the target service.
2. Decide whether the service should reuse shared `pkg/` modules or own its own handler/model/task code.
3. If the service needs its own DB layer, create its own `sql/` and model package and assign a unique migration table name.
4. Reuse shared schemas through the `schemas` config instead of duplicating schema definitions.
5. Run `anclax gen` after spec/SQL/Wire changes.
6. If `examples/` changed, run `make gen`.

## Rules of thumb

- Share `pkg/` code by default; split only where ownership or isolation becomes clearer.
- Keep service bootstrap in `app/<service>`.
- Use app-specific generated package names/paths when multiple services generate similar artifacts.
- Use shared schema files whenever payloads or models overlap across services.
- Prefer clear boundaries over prematurely distributing everything into separate deployables.
