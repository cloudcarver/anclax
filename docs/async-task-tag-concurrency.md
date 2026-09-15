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

Unconfigured tags remain unlimited. Enabling a limit counts attempts already admitted with that tag. Lowering a limit never interrupts existing handlers; usage may exceed the new limit until they finish or expire. Increasing/removing a limit makes capacity available to the next system admission round or manual claim without a worker-config broadcast or waiter update.

## Attempt lifetime

- Automatic, strict, and manual `RunTask` admission all enforce tag limits in addition to local worker capacity, labels, schedules, and serial ordering.
- Automatic admission acquires all required places atomically with a ready reservation and version; Worker claim subsequently binds the execution lease and increments the attempt once. Manual pending claims still combine both steps. A blocked task remains pending and consumes neither an attempt nor a subset of its permits.
- Capacity covers admission through committed finalization. Completion, failure, retry, cron rescheduling, and deferral release it in the same database transaction as the outcome.
- Pause/cancel immediately revoke unconsumed ready reservations. Running attempts retain capacity until the handler exits and finalizes, or its lease expires and is recovered. Resume fences an old running attempt but retains its outstanding permits until lease recovery.
- Editing tags revokes an unconsumed ready reservation. A running attempt instead retains its durable tag snapshot, including tags subsequently removed from its attributes, until finalization.
- Framework config/pause/cancel/broadcast tasks bypass business tag limits so they can make progress when a limit is zero.

Every new task lease records an absolute database expiry and its owner's TTL. Workers with a shorter TTL cannot prematurely recover another worker's lease. Renewal updates only the task row, without writing shared tag counters. Recovery locks the same task row as renewal/finalization, removes its permits exactly once, and fences stale owner/version updates.

This limits admitted database leases. Executors still need to observe context cancellation and make external effects idempotent: a disconnected or uncooperative executor may continue after its lease expires. See [worker leases](async-task-worker-lease.md).

## Claim performance

The schema separates task membership (`task_tags`), configuration (`task_tag_limits`), and independently owned capacity slots (`task_tag_slots`). Read-only `task_tag_concurrency` and `task_tag_permits` views expose usage and attempt ownership; there is no shared `in_use` write. Membership is retained only for pending/ready/running/paused tasks or tasks that still hold a lease. Once a task is terminal and unlocked, its membership is removed in the same transaction; original `attributes.tags` remain available for historical queries. Restoring a task to an executable state rebuilds membership. Configured tag limits remain available for future tasks.

Unlimited tags are task metadata only. They do not create counter-registry rows or permits, acquire tag guards, or update shared counters on admission/finalization. Each admitted task, including a ready reservation, stores its deduplicated admission-time tag snapshot in `lease_tags`. Running-task attribute edits do not change that snapshot; edits revoke an unconsumed ready reservation, and the next admission takes a fresh snapshot. Enabling/re-enabling a limit backfills permits from these snapshots, including paused/cancelled leases pending recovery, in the same transaction that publishes the limit. Removing a limit deletes its slots and usage becomes zero. Slot storage is proportional to the configured capacity (or active owners when larger); a very large finite limit therefore has a configuration/storage cost.

Limit changes acquire an `EXCLUSIVE` task-table lock in a **before-statement** trigger, before locking any limit rows. It serializes configuration/backfill against task locking, insertion, updates, finalization and renewal. Ordinary task transactions do not acquire this exclusive lock. Perform configuration changes in short dedicated transactions, before unrelated row locks, and budget for backfill time on large active workloads. Do not run large backfills inside a long business transaction. Admission and configuration require `READ COMMITTED` (the framework default, also including PostgreSQL's equivalent `READ UNCOMMITTED`); stale-snapshot isolation levels are rejected to prevent missing concurrent admissions/configuration.

Allocation takes a nonblocking advisory guard per free slot, then rechecks ownership in a fresh READ COMMITTED statement. Multi-tag failure rolls back the candidate's partial slots and guards. Finalization and expiry release only their task's slots, without acquiring allocation guards or updating other tasks. When backfill/shrink leaves occupied retired slots beyond the new limit, admission temporarily adds a per-tag overflow guard and occupancy check; releases remain independent. See the [slot protocol](task-admission-design.md) for the invariants and overflow behavior. Direct writes to slot state are unsupported.

Automatic workers have one ready batch claim in flight, independent of execution concurrency. `worker.claimBatchSize` defaults to 32 and accepts 1–256. A durable system task performs pending admission using limited-tag/routing groups and reserves resources before setting tasks ready. The Worker then adopts ready tasks without rechecking tag capacity or serial history. Weighted groups, labels and strict-cap semantics remain enforced.

Ready reservations have a two-second expiry and consume quota until adopted, revoked or recovered. The ready window is bounded by online Worker capacity and capped at 4,096 entries, independently of running task count. Slot release makes capacity available to the next bounded system scheduling round. Cancellation/pause and scheduling edits invalidate unconsumed reservations; quota activation also backfills ready owners. See [ready-state prefetch](ready-task-prefetch.md) for the complete state and recovery protocol.

System tasks use their reserved lane and direct lease-based claim, bypassing business quota/serial/weight admission. TaskStore rejects these unsupported system scheduling attributes. The prefetch/recovery task is itself a system task, so an empty ready queue or full business quota does not prevent scheduling recovery. Legacy/manual pending claim APIs retain full admission.

The prefetch task keeps one execution lease across bounded transactions and immediately follows productive batches to fill the existing ready window. A full ready window is checked after 20 ms; due work with active resource owners receives a 5 ms check. Only idle/quiescent rounds back off to 100 ms. No consumption credits or claim-side demand counters are used. Two control slots preserve system-command progress. Fresh tasks without finite tags or serial keys enter ready through one bulk update, retaining unlimited-tag snapshots for quota backfill; constrained tasks and expired leases use the full allocator.

Scheduler metrics include `anclax_task_scheduler_duration_seconds{operation}`, `anclax_task_scheduler_errors_total{operation,sqlstate}`, `anclax_task_claim_batch_size` (including zero), `anclax_task_finalize_retries_total{sqlstate}`, and `anclax_worker_task_phases{phase}`. Batch-size and error metrics distinguish empty claims, batch progress and exhausted finalization retries. They complement the existing lease-renewal metrics.

## Storage compatibility and upgrade

Migration `0014_task_tag_concurrency` originally introduced the feature and preserves task IDs, payloads, attributes (including duplicate tags), status, attempts, business schedules, and lease versions. Tag backfill includes only `status IN ('pending', 'running', 'paused') OR locked_at IS NOT NULL`. Completed, failed, and cancelled tasks without a lease do not create membership or tag-registry entries. Existing held business leases still count, including paused/cancelled attempts. Existing tasks have no limits until you configure their tags.

Stop old workers, apply migrations, and start only the new workers. Mixed old/new worker versions are unsupported: old SQL does not acquire tag permits or renew the explicit expiry. Legacy leases lack an owner TTL; the first new worker recovers them using the previous `locked_at + configured TTL` rule. Once reclaimed, every new attempt records its own TTL. Filtering backfill avoids historical tag expansion and derived-row writes, but selecting the relevant tasks and building indexes can still scan the task table. The migration still runs inside one transaction and holds its DDL locks until commit, so allow a maintenance window for a large task table.

Migration `0015_task_admission` replaces counters with slots, initializes task-local snapshots from existing permits, removes unlimited permits and persistent wait columns, and adds the lease/head partial indexes. Configuration writes now target `task_tag_limits`; the former counter and permit table names are read-only views, so custom SQL writers and TRUNCATE-based tooling must be updated. Stop old Workers before migration and start only upgraded Workers. Rollback to 14 rebuilds unlimited permits from task snapshots and restores the previous tables, wait columns and functions. Attributes and configured limits remain intact. Migration 15 was merged in PR #71; a development database that already applied an earlier draft must roll back with that draft's binary before applying this version.

Custom model implementations/mocks need the new generated queries and task fields. Business Runner/Executor contracts and persisted task JSON remain unchanged. Database helper functions are internal; execute tasks through the worker, and retain the transaction around generated claim/outcome queries. Treat `InUse` as admitted usage, including expired attempts not yet recovered, rather than a live count of handler goroutines.

For rollback, stop new workers before applying the down migration. Reverting 15 to 14 preserves limits and restores the previous permit protocol. Reverting further to 13 removes the tag-concurrency feature's configuration, membership and permits while preserving task data.

## Verification

The [slot admission measurements](async-task-slot-benchmark.md) compare the shared-counter and slot implementations in three alternating runs at 100/200 execution slots and record the slot implementation's separate 200-iteration chaos run. The [historical v1.4.1 comparison](async-task-admission-benchmark.md) retains the baseline PostgreSQL deadlock log.

```bash
go test -race -tags ut ./... -timeout=6m
ANCLAX_SMOKE_POSTGRES_IMAGE=postgres:17 go test -race -tags=smoke ./pkg/taskcore/e2e \
  -run 'TestTaskSlotAdmissionSmoke|TestTaskAdmission(Migration)?Smoke|TestTaskTagConcurrency(Migration)?Smoke|TestTaskLifecycle(Regressions|Migration)Smoke|TestBatchedTaskLeaseRenewalSmoke' -count=1
ANCLAX_TASKCORE_CHAOS_POSTGRES_IMAGE=postgres:17 \
  ANCLAX_TASKCORE_CHAOS_ITERATIONS=200 go test -tags=smoke ./pkg/taskcore/chaos \
  -run TestContainerizedTaskcoreChaosSmoke -count=1 -v -timeout=30m
```

The container chaos suite mixes limited and unlimited business tasks: approximately one third of batch tasks receive a global tag (limit 3) and a group tag (limit 2). Selection rotates across routing groups and pause/cancel probes without changing the fault RNG, so the remaining workload can exercise worker capacity independently of these caps. The suite records slot allocations durably across database restarts, verifies allocatable slot bounds and task/attempt ownership, and checks that all permits drain after recovery. Its report includes both workload counts and observed global/group peaks. The dedicated PostgreSQL smoke suite separately exercises saturated limits and a large blocked backlog.

Every chaos run first uses executor gates to prove full-limit waiting and release, then forces takeover of specific limited tasks after killing their owner or cutting only that owner's database connections. Initialization retries cannot satisfy these recovery assertions. Claim-path tests combine tags with strict, normal, strict fallback, manual and generic SQL admission, serial ordering, labels and schedules. `make test` includes a short chaos run; use `make chaos` with different seeds for longer local runs and `make taskcore-perf` for sustained-load percentiles. See [test coverage and execution](async-task-testing.md).

Migration `0016_ready_task_prefetch` requires a stopped-Worker upgrade/rollback and updated generated model interfaces. It adds public ready/running states, a resource-group catalog and the internal system scheduler. Rollback releases ready reservations and restores the migration 15 protocol; [upgrade details](ready-task-prefetch.md#upgrade-and-integration).

Ready-prefetch measurements, including unblocked-workload regressions and the final 200-round fault run, are in the [benchmark report](ready-task-prefetch-benchmark.md).

The [long-task prefetch report](long-task-prefetch-benchmark.md) measures the subsequent loop and bulk-admission changes, with fresh chaos and corrected scenario validation.
