# Generated task client sample

This example generates Python and Node.js clients from one task spec. The generated files are checked in so you can inspect or copy them directly. Applications only need their PostgreSQL driver; there is no Anclax SDK package to install.

## Generate

With the Anclax CLI installed, run inside this directory:

```sh
anclax gen
```

You can also specify this directory from elsewhere:

```sh
anclax gen /path/to/anclax/examples/sample
```

To use the CLI source from the repository root:

```sh
go run ./cmd/anclax gen examples/sample
```

The paths in `anclax.yaml` are relative to the sample directory:

```text
api/tasks/tasks.yaml         Task definition
api/schemas/email.yaml       Shared parameter schema
sdks/python/tasks.py         Generated Python client and types
sdks/nodejs/tasks.mjs        Generated Node.js client (ESM)
sdks/nodejs/tasks.d.mts      Generated TypeScript declarations
enqueue.py                  Python usage
enqueue.mjs                 Node.js usage
```

Change the spec or shared schema and run the same command to refresh the clients. This example needs no Go module, sqlc or Wire.

## Enqueue

Use a PostgreSQL database with the Anclax migrations already applied. To execute the task, the Go application must implement `sendEmail` from this spec and run a worker with the `email` label. This example only produces tasks; see [`examples/simple`](../simple) for a Go application.

From this directory, set the database connection and run either language:

```sh
export DATABASE_URL='postgresql://postgres:postgres@localhost:5432/postgres'

# Python 3.11+: uv supplies only the database driver.
uv run --with asyncpg python enqueue.py

# Node.js 18+: the application dependency is only pg.
npm install
npm run enqueue
```

Both scripts print the task ID. They use the same unique tag, so repeated calls from either language return the original ID. Import the generated `Client` and pass a pool, or pass an acquired connection to enqueue within a business transaction.

## End-to-end test

From the repository root, with Go, Node.js/npm, uv and a reachable PostgreSQL server:

```sh
ANCLAX_TASK_CLIENT_TEST_DSN='postgresql://postgres:postgres@localhost:5432/postgres' make test-sample-e2e
```

The database user needs `CREATE DATABASE` permission. The test creates temporary databases and drops them on exit; it does not run against tables in the supplied database. The command installs only the sample's `pg` driver and supplies Python's `asyncpg` through uv.

The test generates clients in a fresh directory using this sample's `anclax.yaml`, checks that the committed clients are current, and runs both enqueue scripts. It starts a real Go worker and waits for `completed`, checking the delivered payload and spec defaults. Both Python-first and Node.js-first scenarios verify cross-language deduplication before and after completion. The test handler records delivery without sending email.

`make test-task-client-integration` also includes this E2E test alongside transaction, concurrency and lightweight-query checks.

See the [client guide](../../docs/async-task-clients.md) / [中文文档](../../docs/async-task-clients.zh.md) for task options, transactions and lightweight queries.
