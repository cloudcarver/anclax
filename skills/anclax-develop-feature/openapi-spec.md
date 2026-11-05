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

Use the input schema to describe accepted JSON values and `x-go-type` to choose
the Go representation. For example:

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

The optional field is `DeviceKey *int64` with `json:"device_key,omitempty"`.
Add `device_key` to the parent's `required` list to generate `int64`. Both `0`
and `"0"` produce a non-nil pointer to zero; an absent field remains nil on a new
model. This also applies to other scalar targets, including `*bool` for an
optional boolean, so missing and explicit false remain distinct.

## Scalar conversion rules

`anyOf` is not required. A single `type: string` with `x-go-type: int32` accepts
only JSON strings and converts their contents to int32. `oneOf`, `allOf`, and
`not` also constrain the original input. To leave the input type unconstrained,
omit `type`; do not write `type: any`. Values must both satisfy the schema and
be convertible to the target type.

| Go target | Convertible JSON inputs | Conversion |
| --- | --- | --- |
| `int`, `int8/16/32/64`, `rune` | number, string | Exact integers, checked against the target width; `int` uses the consuming Go platform's width. |
| `uint`, `uint8/16/32/64`, `byte` | number, string | Exact nonnegative integers, checked against the target width; no signed intermediate restricts uint64. Negative signs, including `-0`, are rejected. |
| `float32/64` | number, string | Decimal conversion with IEEE-754 rounding to the target width, including underflow to zero. Overflow and non-finite values are rejected. |
| `bool` | boolean, string | Strings must be exactly `"true"` or `"false"`; numeric 0/1 and alternate spellings are rejected. |
| `string` | string, number, boolean | Strings are decoded normally; numeric and boolean inputs retain their JSON token text, including number exponents and negative zero. |
| Named custom scalar type | Inputs its decoder or converter accepts | Original scalar constraints are checked before calling its JSON/TextUnmarshaler or an explicit converter. |

Signed integer strings use base-10 `strconv.ParseInt` semantics: an optional
sign and leading zeros are accepted; whitespace, fractions, and exponent notation
are rejected. Unsigned integer strings use base-10 `strconv.ParseUint` semantics
and do not accept signs. JSON numbers such as `123.0` and `1.23e2` are accepted
for integer targets only when their exact value fits. Float strings accept
signed decimal/fraction/exponent notation; hexadecimal, underscores, whitespace,
NaN, and infinity are rejected. Boolean-to-number and object/array-to-string
coercions are not implicit.

Patterns, lengths, enums, bounds, and multiples apply to the original input.
Numeric constraints are checked before float rounding: `0.30000000000000001`
fails `multipleOf: 0.1`, even if a float target would round it to the same value
as `0.3`. Integer conversions cover the full signed/unsigned range. Number-to-string
conversion preserves arbitrarily large numeric tokens without passing them
through float64. Numeric bounds/multiples/enums in the schema itself use the
loader's float64 representation; values beyond ±(2^53−1) are rejected during
generation. Constraints requiring more decimal precision than the loader can
represent should use string patterns/enums instead.

Optional does not imply nullable. Explicit null must be allowed by the input
schema, for example `nullable: true` on a typed branch, and representable by the
Go destination. Optional scalar pointers can become nil. Required built-in
scalar values and scalar array/map elements reject null. Nullable arrays/maps
can become nil. Missing fields on reused destinations retain their previous
values, following `encoding/json` behavior.

### Custom scalar types

A named custom `x-go-type`, such as `time.Time` or an imported identifier type,
uses its existing JSON/Text decoding. Generated named wrappers forward JSON
encoding to the original type as well. The custom type must have a different
name from the generated model. Opaque object/array overrides such as
`json.RawMessage`, and untyped `any`/`interface{}` overrides, retain their existing
behavior.

For a conversion that needs explicit application logic, name a function using
`x-go-converter` and provide its imports through `x-go-type-imports`:

```yaml
key:
  type: string
  pattern: '^k:[0-9]+$'
  x-go-type: identifiers.Key
  x-go-converter: identifiers.ParseKey
  x-go-type-imports:
    - example.com/project/identifiers
```

The function has signature `func(json.RawMessage) (identifiers.Key, error)`.
It receives the original JSON token after scalar schema validation. Its returned
type must match `x-go-type`; Go compilation checks the signature. A converter
selects decoding only; encoding follows the target type's own marshaler or its
normal Go JSON representation. Custom decoders/converters must implement any
application-specific inverse or normalization rules themselves.

### Containers, task payloads, and generated code

Named OpenAPI/shared models and Go task parameters support conversions through
references, recursive objects, arrays, and dictionaries. A pure dictionary with
`additionalProperties: {type: string, x-go-type: uint64}` generates
`map[string]uint64` and converts every value. Nested containers also work. Use
named schemas via `$ref` for nested OpenAPI/shared objects; their existing inline
object representation is `map[string]any`. Task inline objects already generate
named types. Mixed fixed properties plus converted additional properties are
rejected; use a separate dictionary property. Repeated annotated fields,
case-insensitive aliases of those fields, and repeated dictionary keys are rejected.

Generated `UnmarshalJSON` methods contain concrete Go checks and assignments.
Type branches and constant constraints are folded during generation; regexes and
numeric comparison constants are initialized once. There is no runtime schema
interpreter or intermediate JSON marshaling. Shared primitives handle field
capture, exact integer parsing, decimal float strings, and exact numeric
comparisons. Exact comparisons store exponents without expanding powers of ten.
Validation covers annotated scalar values and presence/nullability on paths to
them; it does not enable whole-request schema validation or change YAML/query/form
binding. Standard `json.Unmarshal` and Fiber JSON body binding invoke the methods.

Serialization follows the resulting Go type: for example `"0012"` becomes the
JSON number `12` when stored as int64. Go byte slices (`[]byte`/`[]uint8`)
use their normal base64 string encoding, even when decoded from JSON arrays.
The resulting JSON is not guaranteed to
satisfy the original input schema. Optional converted task fields use `omitempty`.
For tasks containing conversions, generated `New…Task` functions validate the
final serialized payload, after overrides, before returning it. A string-only
input schema targeting int64, or a string branch whose converted number violates
another numeric constraint, returns an error before enqueueing. Task schemas
must also accept their canonical Go JSON output, or use a custom type whose
marshaler emits an accepted representation. HTTP input/output contracts should
be modeled separately when their representations differ.

Python/Node.js task clients retain the input schema and ignore Go-only extensions.
For example, integer-or-string remains `int | str` / `number | string`. JavaScript
callers should send large identifiers as decimal strings to preserve precision.

Run the generated-model decoding benchmarks with:

```sh
ANCLAX_JSON_BENCHMARK=1 go test -v -count=1 ./pkg/codegen/jsondecode -run TestGeneratedScalarDecoders
```

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
