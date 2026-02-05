
# Config usage (anclax.yaml)

Use `anclax.yaml` as the source of truth for generator inputs and outputs. Check it before editing specs or moving files so new work lands in the right packages.

## What to look for

- `oapi-codegen.path`/`out`/`package`: OpenAPI input and generated types.
- `oapi-codegen.config`: Passed through to oapi-codegen as its native config; use it exactly like upstream oapi-codegen configuration.
- `task-handler.path`/`out`/`package`: async task definitions and runner generation.
- `sqlc.path`: database query generation config.
- `wire.path`: DI graph location.
- `clean`: generated output cleanup targets.

## How to use it

1. Confirm the input file path for the area you are changing.
2. Verify the output path/package matches repo conventions (usually `pkg/zgen/...`).
3. After spec/SQL/Wire changes, run `anclax gen`.
4. If you add a new spec or move files, update `anclax.yaml` and keep paths consistent.
