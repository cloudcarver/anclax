# OpenAPI Spec

`anclax gen` generates code from OpenAPI spec in `api/v1.yaml`. The generated code is in `pkg/zgen/apigen`

Follow these rules when writing OpenAPI spec:
1. Always use `required` for required fields. If the field is not required, it will be a pointer type in Go
2. Always define schema in advance, use schema reference (`$ref`) and no inline schema
3. Define clear operationId for each API, which will be the method name in handler
4. Follow the concise part of RESTful API design principles, no need to be strictly RESTful
5. Use HTTP codes for conventional purposes, like 201 for created, 204 for no content, 402 for payment required, etc. Only explicitly define codes when the code is special, codes like 200, 400, 401, 403, 404, 500, ...are implicitly defined if no response body needed.
6. For error message, just send string in body. If it is unexpected error, DO NOT expose the internal error message, just return err and Anclax will log it.
7. Use plural nouns for resource names in paths, like `/users`, `/orders`
