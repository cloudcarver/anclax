# Global concurrency limits by task tag

English | [中文](async-task-tag-concurrency.zh.md)

Task `tags` can carry global concurrency limits shared by every worker using the same Anclax database. Tasks keep their existing tags array; there is no separate `concurrencyKey` field.

For example, configure `tenant:42 = 3` and `vendor:api = 10`. A task tagged with both needs one free place in **each** limit. This limits all tasks with each individual tag, regardless of their other tags. It does not limit only the intersection of the two tag sets.

## Usage

Configure limits through the application's existing worker control plane:

```go
controlPlane := app.GetWorkerControlPlane()
if err := controlPlane.SetTagConcurrencyLimit(ctx, "tenant:42", 3); err != nil {
    return err
}
if err := controlPlane.SetTagConcurrencyLimit(ctx, "vendor:api", 10); err != nil {
    return err
}
```

Keep using `taskcore.WithTags([]string{"tenant:42", "vendor:api"})` on generated runner methods, or `TaskAttributes.Tags` when using the task store directly. Tags are case sensitive; duplicate tags consume one place. Labels still select workers and weighted scheduling groups, and `uniqueTag` still controls enqueue deduplication.

The control plane also provides:

| Method | Behavior |
| --- | --- |
| `SetTagConcurrencyLimit(ctx, tag, max)` | Set or replace a global limit. Zero blocks new attempts. Negative limits and empty tag names are rejected. |
| `RemoveTagConcurrencyLimit(ctx, tag)` | Restore unlimited admission; retain the tag and active usage. Idempotent. |
| `GetTagConcurrency(ctx, tag)` | Return `Tag`, nullable `MaxConcurrency`, and `InUse`. Unknown tags are unlimited with zero usage. |
| `ListTagConcurrencyLimits(ctx, afterTag, pageSize)` | List configured limits in tag order; page size is 1–1000. Start with an empty cursor, then use the last returned tag. |

Unconfigured tags remain unlimited. Enabling a limit counts attempts already admitted with that tag. Lowering a limit never interrupts existing handlers; usage may exceed the new limit until they finish or expire. Increasing/removing a limit wakes waiting tasks without a worker-config broadcast.

## Attempt lifetime

- Automatic, strict, and manual `RunTask` admission all enforce tag limits in addition to local worker capacity, labels, schedules, and serial ordering.
- All required places are acquired atomically with the task lease, owner, attempt count, and lease version. A blocked task remains pending and consumes neither an attempt nor a subset of its permits.
- Capacity covers admission through committed finalization. Completion, failure, retry, cron rescheduling, and deferral release it in the same database transaction as the outcome.
- Pause/cancel requests alone do not release capacity. The handler must exit and finalize, or its lease must expire and be recovered. Resume invalidates the old attempt but retains its outstanding permits until lease recovery.
- Editing tags affects the next attempt. The current attempt releases its durable tag snapshot, including tags subsequently removed from its attributes.
- Framework config/pause/cancel/broadcast tasks bypass business tag limits so they can make progress when a limit is zero.

Every new task lease records an absolute database expiry and its owner's TTL. Workers with a shorter TTL cannot prematurely recover another worker's lease. Renewal updates only the task row, without writing shared tag counters. Recovery locks the same task row as renewal/finalization, removes its permits exactly once, and fences stale owner/version updates.

This limits admitted database leases. Executors still need to observe context cancellation and make external effects idempotent: a disconnected or uncooperative executor may continue after its lease expires. See [worker leases](async-task-worker-lease.md).

## Claim performance

The schema separates task membership (`task_tags`), per-tag counters/configuration (`task_tag_concurrency`), and per-attempt occupancy (`task_tag_permits`). It maintains usage even for unlimited tags, which makes online limit activation correct without scanning running tasks at configuration time. The cost is membership storage and counter writes for tagged attempts, including unlimited tags. Untagged tasks acquire no tag counters.

Business claims inspect at most 32 eligible candidates, locking them on demand and stopping after one admission. Tag rows are locked in lexical order with `FOR NO KEY UPDATE SKIP LOCKED`; contention on a shared tag does not wait while occupying other tag places. PostgreSQL's volatile function snapshots provide current counter values after locks are acquired.

Tasks blocked by a limit leave the partial ready indexes. New tasks whose tag is already full are parked at enqueue time. Release and limit increases use a tag/due-time index to wake at most 64 tasks per tag, capped by available capacity. These are hints: admission always rechecks every tag under locks.

Each worker performs bounded maintenance at most once per 250 ms when claiming or heartbeating. Each sweep visits at most 64 expired new leases, 64 expired legacy leases, and 64 retry waiters. A retry waiter whose blocking tag is still full stays out of the ready index. Transient tag-lock contention gets a 100 ms retry hint; capacity waits get a 5 s hint. These are earliest recheck times, not a wakeup latency guarantee under backlog or lock contention.

The candidate limit bounds tag admission work, not all database work: label matching, serial-head checks, sorting, and skipping locked rows can still examine more rows. A pre-existing ready backlog becomes parked incrementally after a new limit is enabled. Many waiting tags, a very hot shared tag, or large routing groups require workload-specific measurement; strict FIFO/fairness across tag combinations is not guaranteed.

The PostgreSQL smoke suite exercises a backlog of 20,070 parked tasks and one ready task, checks the ready index with `EXPLAIN (ANALYZE, BUFFERS)`, and reports the end-to-end claim time. Local PostgreSQL 17/Docker runs measured approximately 2–3 ms. This is a regression fixture, not a throughput or latency guarantee for production.

## Storage compatibility and upgrade

Migration `0014_task_tag_concurrency` preserves task IDs, payloads, attributes (including duplicate tags), status, attempts, business schedules, and lease versions. It backfills normalized tag membership and counts existing held business leases, including paused/cancelled attempts. Existing tasks have no limits until you configure their tags.

Stop old workers, apply migrations, and start only the new workers. Mixed old/new worker versions are unsupported: old SQL does not acquire tag permits or renew the explicit expiry. Legacy leases lack an owner TTL; the first new worker recovers them using the previous `locked_at + configured TTL` rule. Once reclaimed, every new attempt records its own TTL. The migration creates tables and indexes inside a transaction, so allow a maintenance window for a large task table.

Custom model implementations/mocks need the new generated queries and task fields. Business Runner/Executor contracts and persisted task JSON remain unchanged. Database helper functions are internal; execute tasks through the worker, and retain the transaction around generated claim/outcome queries. Treat `InUse` as admitted usage, including expired attempts not yet recovered, rather than a live count of handler goroutines.

For rollback, stop new workers before applying the down migration. Task data remains, while configured limits, membership indexes, and permits are removed.

## Verification

```bash
go test -race -tags ut ./... -timeout=6m
ANCLAX_SMOKE_POSTGRES_IMAGE=postgres:17 go test -race -tags=smoke ./pkg/taskcore/e2e \
  -run 'TestTaskTagConcurrency(Migration)?Smoke|TestTaskLifecycle(Regressions|Migration)Smoke' -count=1
ANCLAX_TASKCORE_CHAOS_POSTGRES_IMAGE=postgres:17 \
  ANCLAX_TASKCORE_CHAOS_ITERATIONS=200 go test -tags=smoke ./pkg/taskcore/chaos \
  -run TestContainerizedTaskcoreChaosSmoke -count=1 -v -timeout=30m
```

The container chaos suite gives business tasks a global tag and a group tag, records every counter increase durably across database restarts, verifies limits and permit/counter agreement, and checks that all permits drain after recovery.
