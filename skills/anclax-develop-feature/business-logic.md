# Business Logic

Treat external services (DB, APIs, queues) as modules behind minimal interfaces.

Follow these rules when writing business logic:
1. Wrap each integration behind a small interface and hide implementation details.
2. Generate mocks by adding mock config in `anclax.yaml`, then run `anclax gen`. Re-run it after interface changes.
3. Keep business logic in the service layer. Accept and return `apigen` types only.
4. Translate model types to `apigen` types in the service layer.
5. Write unit tests for services using module mocks.
6. Classify errors: expected domain errors vs unexpected errors. Map expected errors in handlers; propagate unexpected errors.
7. Use Wire for dependency injection.
