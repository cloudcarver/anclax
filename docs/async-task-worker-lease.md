# Async Task Worker Leases

Workers execute handlers outside database transactions. PostgreSQL stores task ownership, schedules, outcomes, and events; `Engine` decides admission, while `Runtime` owns asynchronous execution and shutdown.

## Claim and ownership

Each task has `locked_at`, `worker_id`, and a monotonic `lease_version`. A short claim transaction:

1. Selects a due `pending` task whose lease is absent or expired, respecting labels and serial ordering.
2. Locks the candidate with `FOR UPDATE SKIP LOCKED`. A locked candidate does not block unrelated ready tasks; serial successors remain gated by their head task.
3. Sets the owner and database lease timestamp, increments `lease_version` and `attempts`, and commits before execution.

Expiry uses `statement_timestamp()` and the configured TTL. Every renewal, release, and attempt finalization checks both worker ID and lease version. Reusing a worker ID cannot give an old attempt permission to finalize a newer attempt.

The runtime registry is also keyed by `(task ID, lease version)`. A stale attempt's cleanup cannot remove or interrupt a newer attempt. Task-wide cancellation addresses all locally registered attempts for that ID.

## Renewal and finalization

Renewal runs while the executor is active. Temporary database errors may be retried within the last confirmed lease, but the executor context is cancelled when that lease deadline is reached. Renewal shutdown waits for its goroutine before finalization starts.

Finalization computes retry/cron/deferral policy, then uses a short transaction to update status, schedule, attempt count, and ownership atomically. Events share that transaction. Failure hooks use a savepoint so hook SQL errors or panics can roll back independently.

A pause or cancellation already committed in storage wins over a late execution result. Resume is allowed only from `paused` and advances the lease version, invalidating the previous attempt. It retains any outstanding lease until expiry to gate immediate re-execution. Completed, failed, and cancelled tasks remain terminal.

Lease fencing protects task state, not arbitrary external side effects. Executors must observe context cancellation and make repeated side effects idempotent; delivery remains at least once.

## Admission and control tasks

`RunTask` and automatic polling use the same business concurrency and strict capacity. Capacity covers claiming, execution, and finalization. A strict-to-normal fallback retains its strict reservation and rechecks strict arrivals in the fallback SQL statement; normal-only capacity cannot claim strict work. Production polling fills idle slots and refills after completion; empty claims wait for the next polling tick.

Framework config, pause, cancel, and broadcast types have one additional control slot per worker, independent of business priority and strict capacity. Waiting for acknowledgement returns `taskcore.DeferTask(delay)`: it persists the next check and releases the slot without consuming retry attempts. Stable request IDs and unique child tags make repeated invocations idempotent.

## Labels and worker membership

Claims require all task labels to be present on the worker. Unlabelled tasks are eligible for every worker. Each worker adds `worker:<workerID>` to its configured business labels for targeted control messages. Task tags are control-plane selection metadata and do not affect claiming.

Worker registration and heartbeats track availability. Claims use task lease expiry directly. Applied config versions only advance, including during re-registration. Shutdown drains outstanding operations before marking the worker offline, so late registration or heartbeat cannot overwrite the offline marker during a successful drain.

## Configuration and shutdown

Relevant configuration is `worker.pollinterval`, `worker.concurrency`, `worker.heartbeatInterval`, `worker.lockTtl`, `worker.lockRefreshInterval`, `worker.labels`, and optional `worker.workerId`.

Defaults are one-second polling, business concurrency 10, three-second heartbeat/renewal, and a nine-second TTL. TTL must be at least one millisecond; renewal must be non-negative and less than TTL. Zero disables periodic renewal and is primarily useful for explicit lease-expiry tests.

`RuntimeOptions.OperationTimeout` and `ShutdownTimeout` default to five seconds. Shutdown stops admission and cancels running executors, while finalization uses a bounded context independent of caller cancellation. Uncooperative executors may outlive the drain deadline; their tasks recover through lease expiry.

## Migration and verification

Migration `0013_task_attempt_lifecycle` adds task lease versions, unique runtime-config request IDs, and claim/parent indexes. Existing rows begin at lease version zero. Apply the migration and replace all old worker processes before resuming execution: old binaries do not enforce the lease-version guards. Generated Runner/Executor interfaces are unchanged; direct users of generated query parameters must regenerate for the new lease fields.

### Compatibility boundaries

This is not a fully backward-compatible or mixed-version rolling upgrade.

| Surface | Compatibility and required action |
| --- | --- |
| Business task contracts | Generated Runner/Executor methods, `TaskHandler`, `WorkerInterface`, task payloads, and the OpenAPI contract retain their signatures/schema. Keyed `worker.Task` literals remain valid. |
| Lifecycle extensions | `TaskLifeCycleHandlerInterface` now exposes `FinalizeAttempt`; `HandleAttributes`, `HandleFailed`, and `HandleCompleted` were removed. Custom callers/implementations must migrate. |
| Generated queries and models | Claim parameters replace `LockExpiry`/`HasLabels` with `LockTtlMs` and add strict admission controls where applicable. Ownership operations require `LeaseVersion`. The model interface has new methods; custom adapters and mocks must be updated. Structs gained fields, so positional literals may no longer compile. |
| Database | Existing rows and payloads are retained. New binaries require migration 0013. The new schema alone does not make old workers safe: their SQL still lacks attempt fencing. |
| Manual execution | `RunTask` shares automatic business capacity and the strict cap. With strict capacity zero, a strict business task is left pending and `RunTask` returns without claiming it. Calling a stopped runtime no longer starts new work. |
| Concurrency and scheduling | `worker.concurrency` limits business tasks through finalization; built-in control tasks have one extra slot. Polling fills available capacity and refills after completion. Multi-label normal tasks belong to one weighted group: the smallest matching label. |
| Cron and status transitions | Cron attempts reset between occurrences, and an exhausted/failed occurrence does not stop future scheduling. Resume applies only to paused tasks; completed, failed, and cancelled rows remain terminal. Previously failed cron rows are not automatically revived by the migration. |
| Failure hooks and shutdown | Hook errors roll back hook writes to a savepoint while preserving the task result. Executor panics become failures. Shutdown cancellation reschedules interrupted tasks without consuming attempts, and finalization has a separate bounded context. |
| Validation | TTL must be at least 1 ms; renewal must be non-negative and below TTL. Task timeout and retry intervals must be positive. Configurations previously accepted without those checks need correction. |

For an upgrade, stop old workers, apply migration 0013, update any low-level integrations, and start only the new workers. Check configurations and any application reliance on the changed behavior before rollout. A downgrade also needs a coordinated stop and schema rollback; do not mix worker versions against active tasks.

The PostgreSQL regression suite covers control/result races, same-worker stale leases, cron retry budgets, concurrent unique enqueue, hook SQL failures, locked claim contention, config idempotence, and two-worker broadcasts at concurrency one. The migration test inserts tasks and runtime configs at schema version 12, upgrades to 13, verifies retained data, and claims/finalizes an expired legacy lease with the new implementation:

```bash
go test -race ./pkg/taskcore/worker ./pkg/taskcore/dtmtest
ANCLAX_SMOKE_POSTGRES_IMAGE=postgres:17 go test -race -tags=smoke ./pkg/taskcore/e2e -run 'TestTaskLifecycle(Regressions|Migration)Smoke' -count=1
```

Docker tests use port 5499. `make smoke` also runs the regression suite; the image defaults to `postgres:15` unless overridden.
