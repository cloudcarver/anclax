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
| `RemoveTagConcurrencyLimit(ctx, tag)` | Restore unlimited admission; clear its permits and usage. Idempotent. |
| `GetTagConcurrency(ctx, tag)` | Return `Tag`, nullable `MaxConcurrency`, and `InUse`. Unlimited tags report zero usage. |
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

The schema separates task membership (`task_tags`), per-tag counters/configuration (`task_tag_concurrency`), and per-attempt occupancy (`task_tag_permits`). Membership is retained only for pending/running/paused tasks or tasks that still hold a lease. Once a task is terminal and unlocked, its membership is removed in the same transaction; original `attributes.tags` remain available for historical queries. Restoring a task to an executable state rebuilds membership. Configured tag limits remain available for future tasks.

Unlimited tags are task metadata only. They do not create counter-registry rows or permits, acquire tag guards, or update shared counters on admission/finalization. Each leased task stores its deduplicated admission-time tag snapshot in `lease_tags`. Attribute edits do not change that snapshot; a new attempt replaces it. Enabling/re-enabling a limit backfills permits from these snapshots, including paused/cancelled leases pending recovery, in the same transaction that publishes the limit. Removing a limit deletes its permits and resets usage to zero.

Limit changes acquire an `EXCLUSIVE` task-table lock in a **before-statement** trigger, before locking any limit rows. It serializes configuration/backfill against task locking, insertion, updates, finalization and renewal. Ordinary task transactions do not acquire this exclusive lock. Perform configuration changes in short dedicated transactions, before unrelated row locks, and budget for backfill time on large active workloads. Do not run large backfills inside a long business transaction. Admission and configuration require `READ COMMITTED` (the framework default, also including PostgreSQL's equivalent `READ UNCOMMITTED`); stale-snapshot isolation levels are rejected to prevent missing concurrent admissions/configuration.

Task-driven permit changes use nonblocking transaction advisory guards per limited tag, in the namespace `hashtextextended('anclax:task-tag:' || tag, 0)`. Every required guard is obtained before counter changes. A rejected candidate rolls back its own partial guards and mutations; successful candidates retain their guards until the batch commits. Finalization retries a busy guard rather than waiting while holding other tag guards. Raw counter/permit writes outside this protocol are unsupported.

Automatic workers have one batch claim in flight, independent of execution concurrency. `worker.claimBatchSize` defaults to 32 and accepts 1–256. Each query reserves at most the available task slots and strict-priority allowance, checks serial FIFO and labels, and returns up to the requested number. Normal routing groups rotate using the configured weight wheel **per batch**; empty preferred groups can fall back to other groups. A full batch immediately starts another if slots remain; a partial/empty result waits for another poll or completion event. Manual by-ID admission still shares the same local capacity. Built-in control commands retain their independent slot.

The batch query bounds strict candidates by the strict reservation and normal candidates by batch size plus 32. These bounds limit tag admission work, not all label filtering, serial checks, sorting or skipping of locked rows. Serial active-lease checks explicitly exclude rows with neither legacy nor current lease metadata, using `idx_tasks_serial_leased`; FIFO heads use a pending-only ordering index.

Tasks blocked by a limit leave the partial ready indexes. Release only updates permits/counters; it never locks another task to wake it while holding tag guards. Bounded maintenance (at most once per 250 ms when claiming or heartbeating) reaps up to 64 current and 64 legacy expired leases and wakes at most 64 due waiters using the per-tag index. Available capacity is rechecked during maintenance without waiting for the fallback retry timestamp; admission still rechecks all limits. Full tags keep their backlog parked. Wakeup latency depends on polling, backlog and competition; fairness across tag combinations is not guaranteed.

Scheduler metrics include `anclax_task_scheduler_duration_seconds{operation}`, `anclax_task_scheduler_errors_total{operation,sqlstate}`, `anclax_task_claim_batch_size` (including zero), `anclax_task_finalize_retries_total{sqlstate}`, and `anclax_worker_task_phases{phase}`. Batch-size and error metrics distinguish empty claims, batch progress and exhausted finalization retries. They complement the existing lease-renewal metrics.

## Storage compatibility and upgrade

Migration `0014_task_tag_concurrency` originally introduced the feature and preserves task IDs, payloads, attributes (including duplicate tags), status, attempts, business schedules, and lease versions. Tag backfill includes only `status IN ('pending', 'running', 'paused') OR locked_at IS NOT NULL`. Completed, failed, and cancelled tasks without a lease do not create membership or tag-registry entries. Existing held business leases still count, including paused/cancelled attempts. Existing tasks have no limits until you configure their tags.

Stop old workers, apply migrations, and start only the new workers. Mixed old/new worker versions are unsupported: old SQL does not acquire tag permits or renew the explicit expiry. Legacy leases lack an owner TTL; the first new worker recovers them using the previous `locked_at + configured TTL` rule. Once reclaimed, every new attempt records its own TTL. Filtering backfill avoids historical tag expansion and derived-row writes, but selecting the relevant tasks and building indexes can still scan the task table. The migration still runs inside one transaction and holds its DDL locks until commit, so allow a maintenance window for a large task table.

Migration `0015_task_admission` upgrades the lock protocol, initializes task-local snapshots from existing permits, removes unlimited permits/registry rows, and adds the lease/head partial indexes. Stop old workers for this migration and restart only upgraded workers; mixed protocols are unsupported. Rollback to 14 rebuilds unlimited permits from the task snapshots before restoring the old functions. Original task attributes and configured limits remain intact.

Custom model implementations/mocks need the new generated queries and task fields. Business Runner/Executor contracts and persisted task JSON remain unchanged. Database helper functions are internal; execute tasks through the worker, and retain the transaction around generated claim/outcome queries. Treat `InUse` as admitted usage, including expired attempts not yet recovered, rather than a live count of handler goroutines.

For rollback, stop new workers before applying the down migration. Reverting 15 to 14 preserves limits and restores the previous permit protocol. Reverting further to 13 removes the tag-concurrency feature's configuration, membership and permits while preserving task data.

## Verification

```bash
go test -race -tags ut ./... -timeout=6m
ANCLAX_SMOKE_POSTGRES_IMAGE=postgres:17 go test -race -tags=smoke ./pkg/taskcore/e2e \
  -run 'TestTaskAdmission(Migration)?Smoke|TestTaskTagConcurrency(Migration)?Smoke|TestTaskLifecycle(Regressions|Migration)Smoke|TestBatchedTaskLeaseRenewalSmoke' -count=1
ANCLAX_TASKCORE_CHAOS_POSTGRES_IMAGE=postgres:17 \
  ANCLAX_TASKCORE_CHAOS_ITERATIONS=200 go test -tags=smoke ./pkg/taskcore/chaos \
  -run TestContainerizedTaskcoreChaosSmoke -count=1 -v -timeout=30m
```

The container chaos suite mixes limited and unlimited business tasks: approximately one third of batch tasks receive a global tag (limit 3) and a group tag (limit 2). Selection rotates across routing groups and pause/cancel probes without changing the fault RNG, so the remaining workload can exercise worker capacity independently of these caps. The suite records every counter increase durably across database restarts, verifies limits and permit/counter agreement, and checks that all permits drain after recovery. Its report includes both workload counts and observed global/group peaks. The dedicated PostgreSQL smoke suite separately exercises saturated limits and a large blocked backlog.

Every chaos run first uses executor gates to prove full-limit waiting and release, then forces takeover of specific limited tasks after killing their owner or cutting only that owner's database connections. Initialization retries cannot satisfy these recovery assertions. Claim-path tests combine tags with strict, normal, strict fallback, manual and generic SQL admission, serial ordering, labels and schedules. `make test` includes a short chaos run; use `make chaos` with different seeds for longer local runs and `make taskcore-perf` for sustained-load percentiles. See [test coverage and execution](async-task-testing.md).
