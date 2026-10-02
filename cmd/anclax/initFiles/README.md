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
make dev
```

`make dev` runs `dev/setup.sh` to copy the development defaults into `app.yaml` and create a private `.env` containing a random PostgreSQL password, then starts Compose. Existing files are preserved. To run Compose directly, run `sh dev/setup.sh` first. This setup script is ordinary application source and can be customized.

In another terminal (JSON encoding and token extraction use `jq`):

```bash
ANCLAX_USERNAME=your-name
ANCLAX_PASSWORD=$(openssl rand -hex 24)
TOKEN=$(jq -n --arg name "$ANCLAX_USERNAME" --arg password "$ANCLAX_PASSWORD" \
  '{name: $name, password: $password}' | \
  curl -fsS http://localhost:2910/api/v1/auth/sign-up \
    -H "Content-Type: application/json" --data-binary @- | jq -er '.accessToken')
curl http://localhost:2910/api/v1/counter
curl -X POST http://localhost:2910/api/v1/counter -H "Authorization: Bearer $TOKEN"
curl http://localhost:2910/api/v1/counter
```

Choose your username and retain the password for later sign-in. The development `app.yaml` enables simple auth without creating a preset account. Edit that file to customize your application settings. Outside this development configuration, simple auth stays disabled unless explicitly enabled.

The POST returns `202 Accepted`; the worker updates the counter asynchronously, so poll GET until the new value appears. GET returns an object such as `{"count": 1}`.

The database is available to the application over the Compose network and publishes no host port. Run `make db` to open a database console inside the container. `.env` and `.env.*` stay outside Git and the embedded scaffold. Keep `.env` with the database volume: changing the file does not change an existing database password. To migrate an existing development volume, set `.env` to its current password first, then rotate that password in PostgreSQL and update `.env`; creating a new random value alone will prevent login.

## Development

The API honors `anclax.host` and defaults to `localhost:8020`. Compose explicitly sets `MYAPP_ANCLAX_HOST=0.0.0.0` and port 2910 so its published port accepts traffic. Existing container deployments also need an explicit reachable host.

Metrics are disabled by default. To enable local scraping, add `anclax.metrics.enable: true` in `app.yaml`; the listener defaults to `127.0.0.1:9020`. Remote scrapers need an explicit reachable `anclax.metrics.host` and a published port. Pprof is also disabled by default; enabling `anclax.debug.enable` uses `127.0.0.1:8777`, with `anclax.debug.host` controlling exposure. Keep management listeners behind your network access controls.

```bash
anclax install  # install the generators pinned in anclax.yaml
make gen       # regenerate after changing API/task specs, SQL, or Wire providers
make test      # run Go tests
make ut        # run tests with the race detector and coverage
```

Edit `pkg/model/model.go` to extend model behavior. It is scaffold source; only `pkg/model/mock_gen.go`, `pkg/zgen/`, and `app/wire/wire_gen.go` are generated.

## Outbound API client policy

Use `pkg/apiclient` when calling this API from Go. Its `client.go` is ordinary application code: you own the timeout, response limit, HTTP transport, and response handling, and `anclax gen` preserves your changes.

`apiclient.DefaultConfig()` sets a 30-second timeout for the whole request, including reading its body, and a 10 MiB response limit. Edit these defaults in `pkg/apiclient/client.go`, or choose settings for one client:

```go
cfg := apiclient.DefaultConfig()
cfg.Timeout = 2 * time.Minute
cfg.MaxResponseBodyBytes = 64 << 20
client, err := apiclient.New(baseURL, cfg)
if err != nil {
    return err
}
response, err := client.GetCounterWithResponse(ctx)
if err != nil {
    return err
}
```

The size limit applies to both parsed and raw responses. Overflows return `apiclient.ErrResponseBodyTooLarge`, checkable with `errors.Is`. Set `MaxResponseBodyBytes` to zero to disable the size limit, and `Timeout` to zero to disable the request timeout. For large downloads, choose those settings as needed and stream and close the raw response body.

Generated `pkg/zgen/apigen` clients handle the API protocol and accept application HTTP clients through `WithHTTPClient`. Keep custom client policy in `pkg/apiclient`, where regeneration preserves it.

## Transactions and tasks

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
