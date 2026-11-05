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

## Integer or string inputs with an int64 Go field

Combine `anyOf` with `x-go-type: int64` when a JSON input accepts an integer or a
decimal integer string and application code should receive an `int64`:

```yaml
components:
  schemas:
    DeviceRequest:
      type: object
      properties:
        device_key:
          anyOf:
            - type: integer
              format: int64
            - type: string
          x-go-type: int64
```

`device_key` is optional here, so the generated field is
`DeviceKey *int64` with `json:"device_key,omitempty"`. Add it to the parent
object's `required` list to generate `int64`. Missing optional fields remain
`nil` on a new model; both `0` and `"0"` produce a non-nil pointer to zero.

Anclax generates `UnmarshalJSON` methods for affected models. Standard
`json.Unmarshal` and Fiber JSON body binding validate the original annotated
value against its schema, then convert it without passing the input through
`float64`. Strings use base-10 `strconv.ParseInt` semantics: an optional sign and
leading zeros are accepted; whitespace, fractions, and exponent notation inside
strings are rejected. JSON numbers in decimal or exponent notation are accepted
when their exact value is an int64. Invalid values and overflows return errors.
String patterns, lengths, enums, and numeric constraints apply to the original
input, before conversion. Serialization uses the resulting Go value, so int64
fields are written as JSON numbers.

These methods contain schema-specific Go checks and typed assignments. The
generator folds type branches and fixed constraints ahead of time; regular
expressions are compiled once at package initialization. Decoding does not interpret a runtime schema or
marshal intermediate JSON. Only small helpers for capturing annotated fields
and parsing exact int64 numbers are shared. Repeated annotated fields, including
case-insensitive aliases, return errors instead of overwriting an earlier value.

Optional does not mean nullable. An explicit `null` requires a nullable input
schema, for example `nullable: true` on the integer branch. A nullable optional
field becomes `nil`. Required int64 fields and int64 array elements cannot
represent null and reject it. Existing values on a reused destination follow
the usual `encoding/json` rules for missing fields.

This works for named OpenAPI models, configured shared schemas, and Go task
parameters, including `$ref`s and arrays. Use named schemas via `$ref` for
nested OpenAPI/shared objects; their existing inline object representation is
`map[string]any`. Task inline objects already generate named types and are
supported. Named scalar/array shared schemas with conversion become defined Go
types so they can own JSON methods; reference aliases inherit those methods.
Optional task fields containing conversions use `omitempty`, so nil fields
round-trip as absent instead of being emitted as a disallowed null.

The initial conversion target is exactly `x-go-type: int64` combined with
`anyOf`. Other custom types keep their existing decoding behavior. Validation
is scoped to annotated values and the presence/nullability of fields leading to
them; this does not enable whole-request schema validation. JSON methods do not
change YAML, query, or form binding. Dictionaries containing conversions are
rejected at generation time. Since the schema loader stores numeric constraints
as float64, numeric bounds, multiples, and enum values beyond ±(2^53−1) are
rejected at generation time rather than rounded; decimal string enums can express
larger exact values. Input values themselves support the entire int64 range.

Python/Node.js task clients continue to use the input schema and ignore
`x-go-type`; this union becomes `int | str` / `number | string`. JavaScript
callers should send large identifiers as decimal strings to preserve precision.

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
