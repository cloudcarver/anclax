
# Config usage (anclax.yaml)

Use `anclax.yaml` as the source of truth for generator inputs and outputs. Check it before editing specs or moving files so new work lands in the right packages.

`oapi-codegen`, `task-handler`, `sqlc`, and `wire` may each be either a single mapping or an array of mappings. Each array item uses the same schema as the old single-object form, and `anclax gen` runs all configured items in order.

## What to look for

- `schemas.path`/`output`: shared schema YAML directory and generated Go output root used by both OpenAPI and task generation.
- `oapi-codegen` item `path`/`out`/`package`: OpenAPI input and generated types plus generated middleware extensions in the same file. `path` may point to a single spec file or a directory of recursively merged OpenAPI fragments.
- `task-handler` item `path`/`out`/`package`: async task definitions and runner generation.
- `sqlc` item `path`: database query generation config.
- `wire` item `path`: DI graph location.
- `clean`: generated output cleanup targets.

## How to use it

1. Find the matching generator entry for the area you are changing.
2. Confirm that entry's input file path.
3. Verify the output path/package matches repo conventions (usually `pkg/zgen/...`).
4. After spec/SQL/Wire changes, run `anclax gen`.
5. If you add a new spec or move files, update `anclax.yaml` and keep paths consistent.

Example:

```yaml
oapi-codegen:
  - path: api/openapi
    out: pkg/zgen/apigen/spec_gen.go
    package: apigen

sqlc:
  - path: dev/sqlc.yaml

wire:
  - path: ./wire

task-handler:
  - path: api/tasks/tasks.yaml
    out: pkg/zgen/taskgen/runner_gen.go
    package: taskgen
```
