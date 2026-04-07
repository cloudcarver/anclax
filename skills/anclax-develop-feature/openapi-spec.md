# OpenAPI Spec

`anclax gen` generates code from the OpenAPI specs configured under `oapi-codegen` in `anclax.yaml` (commonly `api/openapi`). The `path` can be a single file or a directory of recursively merged OpenAPI fragments. Generated code goes to each configured entry's `out` path, often under `pkg/zgen/apigen`.

Follow these rules when writing OpenAPI spec:
1. Use `required` for required fields. Non-required fields become pointer types in Go.
2. Define schemas under `components/schemas` and reference them with `$ref`. Avoid inline schemas.
3. Set clear `operationId` values; they map to handler method names.
4. Follow pragmatic REST conventions; strict REST is not required.
5. Use HTTP status codes conventionally (201 created, 204 no content, 402 payment required). Define explicit responses only when a response body is needed or behavior is special.
6. For error messages, return a string body for expected errors. For unexpected errors, return the error and let Anclax log details without exposing internals.
7. Use plural nouns in paths (e.g., `/users`, `/orders`).
