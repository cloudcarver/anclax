# Task admission design

The v1.4.1 production report identified claim/finalize deadlocks involving shared tag counters and expensive serial-history checks. Execution slots also determined concurrent automatic claim queries. Migration 15 replaces shared accounting with independent capacity slots and removes persistent task wait flags; bounded batches control database admission separately from execution capacity.

## Slot ownership and lock protocol

`task_tag_limits` stores configuration. `task_tag_slots` stores one row per configured place, with an optional task ID and lease version. Partial indexes select free slots, owned slots and retired overflow. `task_tag_permits` and `task_tag_concurrency` are read-only compatibility views; `InUse` is computed from occupied slots, never updated by a task transaction. Unlimited tags have membership and attempt snapshots but no slots.

Claim locks task rows first. Each candidate allocates one slot per limited tag atomically. Allocation uses a nonblocking transaction advisory guard for each candidate slot, in the namespace `hashtextextended('anclax:task-slot:' || tag || ':' || slot_no, 0)`. A separate READ COMMITTED statement rechecks that the slot is still free after acquiring the guard. A stale cursor rolls back that slot's guard; failure to obtain all tags rolls back the candidate's partial slots and guards, including release of its previous expired attempt. Earlier successfully admitted tasks in the batch retain their allocations until commit.

Finalization and expiry hold the task row and clear only slots owned by that task. They acquire neither allocation guards nor shared tag guards, and never update another waiting task. Allocators mutate only free slots under their per-slot guards, whereas releases mutate only their task's occupied slots. Configuration cannot interleave with either path. These invariants remove the shared tag-row write dependency between different tasks. Slot/metadata writes outside the framework protocol are unsupported. Hash collisions can conservatively reject allocation without exceeding capacity.

## Configuration and overflow

A before-statement trigger on configuration takes an `EXCLUSIVE` task-table lock before any configuration-row lock. It drains task transactions, rebuilds slots from durable `lease_tags` snapshots and publishes the limit atomically. Configuration requires READ COMMITTED to see commits that occurred while waiting, and must run in a short dedicated transaction before unrelated row locks. Snapshot tags are stable through attribute edits and pause/resume fencing. Removing a limit deletes its slots; re-enabling backfills all outstanding attempt snapshots, including ready reservations and paused/cancelled or expired leases awaiting recovery.

Resizing packs existing owners first. When usage exceeds the new limit, excess occupied slots are marked retired and can only be released. During this exceptional overflow period, admission additionally uses a nonblocking per-tag overflow guard and a fresh occupancy count; this prevents filling nominally free slots while total usage is still at/above the limit. Releases do not acquire this guard. Admission resumes as soon as usage is below the limit, even if one of the remaining owners occupies a retired slot. With no occupied overflow, normal admission uses only independent slot guards.

Slot storage is proportional to the configured limit, or outstanding owners when larger. Configuring a very large finite limit materializes that many rows; use unlimited admission when no bound is needed. Configuration/backfill can delay scheduling and renewal and needs a suitable maintenance window at large scale.

## Database admission and ready reservations

Migration 16 adds a durable `ready` phase in the existing task table. A reserved system lane directly executes the long-lived singleton `prefetchTasks` job, which performs bounded pending admission and expiry recovery in separate short transactions. It polls after an empty round and retains its execution lease across rounds; a second control slot remains available for commands. Resource-group indexes let it skip a full limited-tag combination as a unit. Successful admission reserves slots and serial ownership before publishing ready rows. Fresh tasks without limited tags or serial keys use a bulk ready update; expired owners and constrained tasks retain full admission. Unlimited tags do not split otherwise identical routing groups.

The long scheduler prioritizes supply: productive batches continue immediately until the existing PostgreSQL ready cap or resource admission stops them. Structured wait reasons distinguish full supply, active resource blocking, idle and quiescent work. Short busy checks coexist with bounded idle backoff, without consumption credits or a claim-side counter write. See [supply-first scheduling](ready-task-prefetch.md#supply-first-scheduling) for the policy and tradeoffs.

Automatic Workers keep one ready batch claim in flight, independent of execution capacity. The default batch remains 32. Claim checks routing/strict allowance and a valid ready reservation, then transfers the existing resources and version into a running execution. It neither scans pending backlog nor repeats resource/serial admission. Normal group weights are honored at both preparation and consumption. Legacy/manual pending claim APIs remain available; known-ID requests can adopt a ready reservation.

Ready reservations expire after two seconds and count toward tag usage. Cancel, pause and scheduling edits revoke an unconsumed reservation atomically; execution snapshots remain stable through fencing. The ready window is bounded separately from the number of executing tasks. The [ready-prefetch design](ready-task-prefetch.md) describes system isolation, worker registration, configuration backfill, rollback and remaining scan/queue tradeoffs.

## Finalization

Handler completion retains its Worker slot and lease registration through committed finalization. Each outcome transaction suspends and drains that attempt's renewal to avoid racing its own row update, using the last confirmed lease deadline. A failed, explicitly rolled-back transaction resumes renewal and retries SQLSTATE `40P01`, `40001`, or `55P03` with jitter, within five seconds and the caller's deadline. Slot release itself does not produce a tag-busy retry. The handler is not rerun. Uncertain connection/commit outcomes are not retried as known rollbacks. At-least-once recovery and external-effect idempotency still apply.

## Verification boundaries

PostgreSQL tests cover updated/uncommitted slot versions, independent releases while allocation guards are held, multi-tag partial rollback, concurrent batches/finalizers, quota activation and shrink overflow, version fencing, migrations 14→15→14, 100,000 serial-history rows, and 100 Worker slots filled by four queries. An injected 900 ms transaction rollback window outlasts a 600 ms lease while renewal continues and the handler executes once. Container fault tests cover owner death, partition, recovery and quota removal/re-enabling, with durable allocation auditing.

The [slot measurements](async-task-slot-benchmark.md) record the final implementation's three-run automatic comparison, a fresh 200-iteration chaos run and the remaining blocked-backlog filtering cost. The [historical comparison](async-task-admission-benchmark.md) contains a local baseline claim/finalize deadlock involving the same SQL pair and tag-counter relation as the production report. Its `caf7332` results describe the earlier shared-counter PR implementation, not this slot protocol. Measurements must identify their source revision; synthetic local tests do not establish production capacity. Old/new protocols must not run together during migration or rollback; see [upgrade instructions](async-task-tag-concurrency.md#storage-compatibility-and-upgrade).

Ready-prefetch measurements are split between the [initial comparison](ready-task-prefetch-benchmark.md) and the [long-task/bulk-admission follow-up](long-task-prefetch-benchmark.md). Each report identifies its baseline, validation and remaining costs.
