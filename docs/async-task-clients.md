# Generated Python and Node.js task clients

`anclax gen` produces self-contained client source files that enqueue tasks directly into the PostgreSQL database used by Go workers. The output includes parameter types, task options, SQL and runtime logic. Applications import these files directly; there is no Anclax Python or npm package to install.

The generated code supports Python 3.11+ and Node.js 18+. It accepts an application's existing asyncpg / node-postgres connection or pool. If the application does not already have its database driver, install that driver:

```sh
python -m pip install asyncpg
npm install pg
```

## Generate a client

A complete example is available in [`examples/sample`](../examples/sample). From the repository root, run `go run ./cmd/anclax gen examples/sample` to generate its clients into `examples/sample/sdks/python` and `examples/sample/sdks/nodejs`.

Add entries to `anclax.yaml` alongside the Go `task-handler`, using the same task spec:

```yaml
task-client:
  - language: python
    path: api/tasks/tasks.yaml
    out: generated/tasks.py
  - language: nodejs
    path: api/tasks/tasks.yaml
    out: generated/tasks.mjs
```

Run `anclax gen`. `task-client` accepts a single mapping or a list. Projects that only generate these clients need neither `go.mod` nor sqlc/Wire. Paths are relative to the generation working directory. The CLI embeds its runtime templates and SQL; generating clients needs only the task spec and its referenced schemas. Running the generated output needs neither the CLI nor the original spec.

Python generates a single `.py` file containing `TypedDict` parameters, `TaskOptions`, `TaskClient` and `Client.run_<snake_case_task_name>` methods. Node.js generates one JavaScript file containing `TaskClient` and `Client.run<TaskName>` methods, plus TypeScript declarations for the complete client API. Use `.mjs` for ESM (`.d.mts`), `.cjs` for CommonJS (`.d.cts`), or `.js` for CommonJS in a package without `"type": "module"` (`.d.ts`). Each generated client is independent and can be copied or committed to the downstream repository.

Apply framework migrations through the Go application before enqueueing tasks. Pin the CLI to a revision compatible with the Go worker, and rerun `anclax gen` when updating the spec or client logic. Generated files belong to the application, so updates are explicit and reviewable in its source control.

Supported parameter schemas include objects, required/optional fields, nested objects, arrays, dictionaries, scalars, enums, nullable values, `oneOf`/`anyOf`, and local `$ref`s, including `file.yaml#schemas/Name`. References resolve relative to their containing file. Recursive references and `allOf` fail generation explicitly. Dates and UUIDs use JSON strings. Types provide static checks, not runtime JSON Schema validation or parameter default filling. Go-only type extensions are ignored. Omitted `parameters` preserves Go's implicit `{taskID: integer}` event payload; specify `type: object, properties: {}` for empty parameters.

For integer-or-string parameters annotated with `x-go-type: int64`, the generated
Go worker types validate and convert JSON input to int64; Python and Node.js
clients retain the input union. Send int64 values outside JavaScript's safe
integer range as decimal strings. See [JSON type conversion](../skills/anclax-develop-feature/openapi-spec.md#integer-or-string-inputs-with-an-int64-go-field).

For a `sendEmail` task with `userID` and `template` parameters:

```python
from generated.tasks import Client, TaskOptions

# pool is an asyncpg.Pool owned by your application.
task_id = await Client(pool).run_send_email(
    {"userID": 123, "template": "welcome"},
    options=TaskOptions(unique_tag="welcome:123", attributes={"tags": ["tenant:acme"]}),
)
```

```javascript
import { Client } from "./generated/tasks.mjs";

// pool is a pg.Pool owned by your application.
const taskID = await new Client(pool).runSendEmail(
  { userID: 123, template: "welcome" },
  { uniqueTag: "welcome:123", attributes: { tags: ["tenant:acme"] } },
);
```

## Transactions and options

Pass an acquired connection to include enqueueing in a business transaction. The client never begins, commits, rolls back or closes connections itself. Passing a pool delegates individual queries to the pool; transactions require a single connection, per [asyncpg](https://magicstack.github.io/asyncpg/current/usage.html#transactions) and [node-postgres](https://node-postgres.com/features/transactions).

```python
async with pool.acquire() as conn, conn.transaction():
    await conn.execute("INSERT INTO welcome_requests(user_id) VALUES ($1)", 123)
    await Client(conn).run_send_email({"userID": 123, "template": "welcome"})
```

```javascript
const conn = await pool.connect();
try {
  await conn.query("BEGIN");
  await conn.query("INSERT INTO welcome_requests(user_id) VALUES ($1)", [123]);
  await new Client(conn).runSendEmail({ userID: 123, template: "welcome" });
  await conn.query("COMMIT");
} catch (error) {
  await conn.query("ROLLBACK");
  throw error;
} finally {
  conn.release();
}
```

| Option | Python | Node.js |
| --- | --- | --- |
| Deduplication key | `unique_tag` | `uniqueTag` |
| Parent ID | `parent_task_id` | `parentTaskId` |
| Absolute schedule | `started_at` (aware `datetime`) | `startedAt` (`Date`) |
| Override spec delay | `delay` (`timedelta`) | `delayMs` (number) |
| Override spec attributes | `attributes` | `attributes` |

Both languages use wire field names in `attributes`: `timeout`, `retryPolicy`, `cronjob`, `labels`, `tags`, `priority`, `weight`, `serialKey`, `serialID`, `releaseUniqueTagOnTerminal`. Overrides merge at the top level, replacing a supplied retry policy completely. Absolute schedules override the spec delay and cannot be combined with an explicit delay option. Delay options replace the spec delay; no delay leaves `started_at` null. As with Go, a cron definition alone does not postpone the first run; the Go worker schedules subsequent runs.

Enqueueing returns an ID. An existing unique tag returns its original task's ID without modifying that task. Concurrent inserts use the unique constraint and a fresh lookup under READ COMMITTED. Database errors, including serialization failures at stronger isolation levels, propagate to callers. Unique tags remain after termination unless `releaseUniqueTagOnTerminal` is enabled. Worker-only `prefetchTasks` and `anclax:system:` unique tags are rejected.

Use default asyncpg JSON codecs / node-postgres type parsers. Payloads are ordinary JSON, not base64. Python supports full-size integers. Node.js rejects unsafe JavaScript integers; use schema-defined strings for larger identifiers. Bound database waits with driver configuration or caller deadlines. For dynamic tasks, `TaskClient.enqueue(...)` is also available; generated methods retain spec defaults and parameter types.

## Lightweight operations

| Python | Node.js | Result |
| --- | --- | --- |
| `get_task_by_id(id)` | `getTaskByID(id)` | Task or `None` / `null` |
| `get_task_by_unique_tag(tag)` | `getTaskByUniqueTag(tag)` | Task or `None` / `null` |
| `get_last_task_error_event(id)` | `getLastTaskErrorEvent(id)` | Latest error or `None` / `null` |
| `update_pending_task_priority_by_labels(labels, priority)` | `updatePendingTaskPriorityByLabels(labels, priority)` | Affected row count |
| `update_pending_task_weight_by_labels(labels, weight)` | `updatePendingTaskWeightByLabels(labels, weight)` | Affected row count |

Reads use database column names (`id`, `unique_tag`, etc.) with decoded `spec` and `attributes`. Updates match **any** supplied label; an empty list selects unlabeled tasks. Only `pending` rows change, excluding `ready`, running and terminal tasks. Scheduling columns and JSON attributes update atomically. There are no client-side scans; a large matching set can still make SQL bulk updates expensive.

Pause/cancel, descendant cascades, worker interruption, waiting, cron recalculation and scheduling remain in the Go control plane. A bare status update would not provide their full semantics.

## Development checks

Runtime templates live in `pkg/codegen/task/client_templates`. SQL is taken directly from the Go store's `sql/queries/tasks.sql` when the CLI is built and included in generated clients. There is no separate SQL generation step or runtime package build.

```sh
make test-task-clients
npm --prefix pkg/codegen/task/clienttests ci
ANCLAX_TASK_CLIENT_TEST_DSN=postgresql://postgres:postgres@localhost:5432/postgres make test-task-client-integration
```

Unit tests execute the generated clients in an isolated directory without Anclax packages. Integration tests require database creation rights. The harness creates and drops a random database, applies real migrations, type-checks generated clients, exercises transactions/concurrent deduplication/updates and passes their tasks through the Go store and handler, including terminal unique-tag release. Any reachable PostgreSQL instance works; Docker is optional. The private npm manifest under `clienttests` supplies test dependencies only.

The integration target also runs the [`examples/sample` E2E test](../examples/sample/README.md#end-to-end-test): generate fresh clients through the CLI, run both example producers, then execute their task through a real Go worker to completion. To run only that path, use `ANCLAX_TASK_CLIENT_TEST_DSN=... make test-sample-e2e`.
