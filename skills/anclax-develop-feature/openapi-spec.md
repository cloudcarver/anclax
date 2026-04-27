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

## Middleware Extensions

Use `x-check-rules`, `x-functions`, and security scopes when route-level auth or validation should be generated from the OpenAPI spec.

- `x-check-rules` define validation/authorization function signatures. They generate `Validator` methods that return `error`.
- `x-functions` define utility function signatures. They generate `Validator` methods that return the declared value and can be called from security scopes.
- Security scopes contain Go expressions, not a custom DSL. Call generated methods with `x.MethodName(...)`; path parameters and generated values such as `operationID` can be referenced directly when available.
- These extensions define the interface only. Implement the generated `Validator` methods in application code after running `anclax gen`.
- Keep functions focused and name them by intent, such as `ValidateOrgAccess`, `OperationPermit`, or `GetCurrentOrgID`.

Example:

```yaml
x-check-rules:
  ValidateOrgAccess:
    description: Validate that the user can access an organization.
    useContext: true
    parameters:
      - name: orgID
        schema:
          type: integer
          format: int32
      - name: requiredRole
        schema:
          type: string

x-functions:
  GetCurrentOrgID:
    description: Get the organization ID from the current context.
    useContext: true
    return:
      name: orgID
      schema:
        type: integer
        format: int32

paths:
  /orgs/{orgID}/projects:
    get:
      operationId: ListProjects
      security:
        - BearerAuth:
            - x.ValidateOrgAccess(c, orgID, "viewer")
            - x.ValidateOrgAccess(c, x.GetCurrentOrgID(c), "viewer")
```

For complete examples, see `docs/middleware.md`.
