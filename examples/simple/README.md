# myexampleapp

This project is initialized by `anclax init`.

Use skill:

```
npx skills add cloudcarver/anclax
```

## File structure

- `app/`: application bootstrap. The default template keeps one app here. In a larger repo, you can grow this into `app/service_a`, `app/service_b`, and so on. Each service can have its own `app.go`, `injection.go`, and `wire/`.
- `api/`: OpenAPI fragments under `api/openapi/`, task definitions under `api/tasks/`, and shared schemas under `api/schemas/`.
- `pkg/`: reusable modules shared by apps and services.
- `sql/`: shared queries and migrations for the default shared model.
- `pkg/zgen/`: generated code. Do not edit by hand.
- `.anclax/bin/`: pinned external tools used by `anclax gen`.

## Quick test

```bash
docker compose up
```

In another terminal (the token extraction uses `jq`):

```bash
curl http://localhost:2910/api/v1/counter
TOKEN=$(curl -fsS http://localhost:2910/api/v1/auth/sign-in \
  -H "Content-Type: application/json" \
  -d '{"name": "test", "password": "test"}' | jq -r '.accessToken')
curl -X POST http://localhost:2910/api/v1/counter -H "Authorization: Bearer $TOKEN"
curl http://localhost:2910/api/v1/counter
```

The POST returns `202 Accepted`; the worker updates the counter asynchronously, so poll GET until the new value appears. GET returns an object such as `{"count": 1}`.

Compose mounts `dev/app.yaml` as `app.yaml` to enable simple auth and create the `test` / `test` development account. Account creation is safe across restarts. Outside this development configuration, the scaffold does not create a test account or enable simple auth automatically.

## Development

```bash
anclax install  # install the generators pinned in anclax.yaml
make gen       # regenerate after changing API/task specs, SQL, or Wire providers
make test      # run Go tests
make ut        # run tests with the race detector and coverage
```

Edit `pkg/model/model.go` to extend model behavior. It is scaffold source; only `pkg/model/mock_gen.go`, `pkg/zgen/`, and `app/wire/wire_gen.go` are generated.

Use `core.Tx` from `github.com/cloudcarver/anclax/core` for transaction callbacks and `SpawnWithTx`. This matches generated task runners and failure hooks:

```go
err := m.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
    // Combine your business writes through txm with task enqueueing.
    _, err := runner.RunIncrementCounterWithTx(ctx, tx, &counter.IncrementCounterParams{Amount: 1})
    return err
})
```

An `onFailed` hook receives an existing `core.Tx`; use `m.SpawnWithTx(tx)` inside it. Executors run outside that transaction. The counter demonstrates task delivery and honors `Amount`; it can increment again if delivery is repeated. For business operations that must apply once, persist an idempotency key with the write. The `AutoIncrementCounter` task definition is available as a cron example and must be enqueued explicitly to start it.

## Multi-service pattern

Anclax works well for service-oriented repos in a single codebase:

- Keep reusable modules in `pkg/`.
- By default, all apps can share `pkg/model` and the top-level `sql/` folder.
- If one service needs its own persistence layer, give it its own `sql/` folder and model package under `app/<service>/`, and use a unique migration table name so its migrations do not conflict with others.
- A service may also keep its own handlers, task executors, and API definitions under `app/<service>/`.
- Use the shared `schemas` configuration in `anclax.yaml` to reuse schema definitions across services.
