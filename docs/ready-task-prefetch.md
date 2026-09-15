# Ready-state task prefetch

Migration 16 separates business admission from Worker execution. The `tasks` table is also the durable ready queue; there is no second queue-membership table. `ready` means all finite tag slots and serial exclusion have been reserved, but no Worker owns the execution yet. `running` identifies automatic business execution and system execution. Legacy/manual pending claims remain supported with their existing lease representation.

## State and ownership

| Phase | Resources | Worker lease | Attempts |
| --- | --- | --- | --- |
| pending | None, except an older fenced attempt awaiting recovery | None, or an older attempt awaiting recovery | Unchanged |
| ready | All limited tags, plus serial ownership | None; separate reservation expiry | Unchanged |
| running | The same reserved resources | Bound to the consuming Worker | Incremented once |

Admission locks the candidate, atomically allocates every required independent tag slot, records the admission-time tag snapshot and version, and changes it to ready. Failure leaves it pending without partial allocation. Worker claim locks ready rows, checks routing/strict allowance and reservation validity, and adopts the existing version and resources in one transaction. It does not repeat tag allocation or serial-history checks. Manual by-ID execution can adopt a ready reservation through the same ownership rules.

A ready reservation lasts two seconds. Expiry returns it to pending and releases its slots under the same task row lock used by claim. Cancellation, pause, or edits to scheduling attributes revoke an unconsumed reservation; an old version cannot reclaim the new owner's resources. Running attempts retain their original tag snapshot through attribute changes and pause/resume fencing. Finalization and renewal retain the independent-slot protocol introduced in migration 15.

Ready owners participate in serial exclusion and FIFO checks. Enabling a quota backfills ready owners as well as outstanding execution leases. Reducing a quota retains existing reservations, including retired overflow, just as it retains existing executions. Unlimited tags do not allocate slots or counters.

## System execution

The framework bootstraps a durable `prefetchTasks` task with unique tag `anclax:system:prefetch`. It uses the reserved control/system lane and claims directly from pending or expired running system tasks. It can start with an empty ready set or exhausted business quotas. Every scheduling round checks the system task's owner, lease version, state and deadline while holding its row lock. Repeating or resubmitting another task of this type does not create another scheduler.

A round recovers at most 256 expired ready reservations and performs the existing bounded execution-lease recovery. It then admits at most 256 tasks. The system task is long-lived and runs successive bounded transactions under one execution lease. Consumption pacing determines batch size and the next start time, with all waits outside a transaction. The normal lease keeper renews it while busy or idle. There is no per-round claim/finalize cycle and no new notification mechanism. The integrated Worker reserves two control slots, so the singleton scheduler leaves capacity for cancellation, pause and configuration commands.

## Consumption pacing

Each nonempty automatic ready claim increments its registered Worker's `prefetch_claimed` counter in the same SQL transaction as the task leases. Rollbacks and empty claims do not increment it. Counters use separate Worker rows; there is no global consumption counter or unlimited-tag counter. The scheduler reads these small rows to estimate demand, including offline counters so liveness changes do not replay old consumption. Historical Worker rows still cost a read; large Worker churn is not covered by the single-Worker benchmark.

On startup, the scheduler grants one live-capacity window, ignoring historical counter values. Subsequent committed claims add refill credit; only successful admission spends it. Credit is bounded at 4,096 and a call requests at most 256 tasks. Counters are scheduling hints: resource allocation, routing, the existing ready cap and lease fencing remain authoritative in PostgreSQL.

The controller smooths the consumption rate over 200 ms and aims for half the smoothed nonzero consumption burst, capped at 32 tasks, when calculating the next interval. Intervals are bounded to 5–250 ms and include time spent in SQL. The interval target can be fractional; actual task counts are integers. A fixed `32 / rate` interval can feed prefetch-induced slowdowns back into the rate, particularly for small finite quotas, so it is deliberately avoided. Actual refill volume follows credit rather than this interval target.

With no credit, the scheduler skips expensive admission until a small periodic probe is due. Empty attempts back off through 20, 40, 80, 160 and 250 ms. Probes request at most four tasks and target at most 250 ms between admission starts, subject to database/scheduler delay. They discover new arrivals, recover expired reservations and detect stale scheduler ownership even when consumption is zero. A productive idle probe grants one new capacity window to restore throughput. Legacy/manual claims without a registered automatic Worker do not report consumption and rely on these probes for progress. Long-idle detection therefore trades roughly four admission attempts per second for up to 250 ms of polling delay; it does not eliminate all idle SQL.

Cancellation, a database error or lost scheduler ownership ends the loop. Graceful Worker shutdown defers the durable task back to pending without accumulating an attempt; an expired owner can be taken over through the ordinary system claim path. Every batch checks the durable fence, so even a caller without a lease keeper stops after losing ownership. System claim orders by the next due time, then ID.

System tasks retain time eligibility, labels, execution leases, renewal, retries and cancellation. They bypass business tag quotas, serial scheduling and weighted admission. TaskStore rejects nonempty tags, serial keys and non-default weights for system tasks. The `prefetchTasks` type and `anclax:system:` unique-tag prefix cannot be enqueued through TaskStore; bootstrap is framework-owned. System task types are framework-owned; high business priority does not confer system status.

## Candidate selection and bounds

`task_admission_groups` is a catalog of normalized **limited** tag sets and required label sets. Task insertion/attribute changes assign a group using indexed lookup; ordinary lifecycle operations do not update a shared group counter. New group creation uses a nonblocking creation guard: concurrent insertions that cannot acquire it commit with an unclassified membership, and the system task classifies up to 256 such rows through a partial index. This prevents opposite multi-task insertion orders from creating unique-key lock cycles. Distinct unlimited tag sets share a group when their routing labels match. The existing configuration barrier reclassifies active tasks when limits are enabled or removed.

The scheduler computes available slot counts once, skips full resource groups, and reads a bounded candidate prefix through each group's pending index. Candidate limits also use the smallest currently available tag capacity. The allocator remains authoritative when the snapshot becomes stale. This avoids revisiting every task in a full group on each Worker claim. The sorted candidate prefix is bounded before row locking; a locking lateral lookup forces per-candidate primary-key access instead of allowing a full-table join scan. A row-version check rejects candidates edited concurrently before their lock is acquired.

Within that ordered prefix, fresh tasks with no limited tags, no serial key and no old execution lease are admitted by one bulk UPDATE. They retain reservation versions and admission-time tag snapshots, including unlimited tags needed for later quota activation. Constrained tasks and expired owners still use the full allocator; an expired owner may need to release slots from its previous snapshot even when its current tags are unlimited. Admission requires READ COMMITTED, and the task-table configuration barrier prevents quota changes during either path. The durable group cursor is written only when its value changes.

Strict priorities retain creation order for ties. Normal scheduling uses the existing sorted weighted-group wheel, with a durable cursor on the singleton system task; weights do not require expanding a large wheel in SQL. Worker batches retain group preference/fallback and strict-cap enforcement. The scheduler uses the same rounded-up strict allowance as the Worker, including a single Worker slot with a positive percentage.

The ready window is bounded by registered online Worker capacity, capped at 4,096 ready tasks globally; this is a prefetch window, not a limit on executing tasks. Worker routing and heartbeat freshness constrain candidate eligibility. Ready reservations can occupy quota while waiting for a compatible Worker; this is a real tradeoff of pre-admission. Runtime configuration acknowledgments update the scheduler's strict-cap metadata.

The system admission function disables JIT locally. Its bounded lateral query incurred disproportionate compilation time in the initial prototype; the setting does not change other database queries. Large numbers of distinct resource/routing groups, serial-blocked candidates within an available group, and empty catalog groups can still add scheduling work. The global scheduler's own throughput is also a capacity limit and must be measured independently of Worker capacity.

## Upgrade and integration

Stop old Workers, apply migration 16, then start only the new Workers. Do not mix old and new protocols. Migration 16 adds the ready indexes/catalog and backfills active group membership; this requires a maintenance window on large task tables. Migrations 14 and 15 remain unchanged.

Rollback requires stopping Workers first. The down migration returns ready reservations to pending, releases their slots, maps running execution back to the legacy pending lease representation, removes the internal scheduler task and restores migration 15 functions. Existing execution leases and original task attributes remain intact.

Use `BuildWorkerComponents` / `NewWorkerFromConfig` for the integrated scheduler lifecycle. Custom models must implement the newly generated queries. Custom runtimes must register their capacity with `ConfigureWorkerPrefetch`, bootstrap `EnsureTaskPrefetch`, and reserve system execution capacity for both the long-lived scheduler and other control commands; direct calls to the ready-only batch query do not perform admission themselves. Legacy/manual pending claim APIs retain full admission for compatibility.

Operationally, report ready depth/age separately from pending and executing tasks. Tag `InUse` includes ready reservations and outstanding executions, including expired owners awaiting recovery. The scheduler duration/error metrics include `operation="prefetch"` for each batch transaction and `operation="prefetch_consumption"` for counter reads, independently of the long-lived task's claim/finalize lifecycle. Track total PostgreSQL CPU/SQL work, successful completions, ready expiration and group cardinality alongside the smaller Worker claim latency.

## Verification

PostgreSQL regressions cover reservation transfer without a second attempt/allocation, expiry versus a locked claim, cancel/pause/attribute edits, quota activation and overflow, serial ownership, weighted progress under one shared slot, stale scheduler fencing and unlimited-tag group normalization. Existing lifecycle, migration, renewal and concurrent batch tests also run against the new protocol. Benchmark evidence must include enqueue/group-maintenance cost and system scheduler work, not only Worker claim time.

Long-task regressions verify repeated work under one attempt, idle renewal beyond the lease TTL, simultaneous control progress, shutdown and fencing. A statement-level audit verifies bulk admission, mixed constrained candidates, snapshots and quota backfill. An expired-lease regression exhausts the maintenance prefix before verifying that the full allocator releases the old reservation.

The [initial benchmark](ready-task-prefetch-benchmark.md) records the backlog benefits and unblocked-workload regressions of one-round prefetch. The [long-task follow-up](long-task-prefetch-benchmark.md) compares the loop and bulk-admission changes with that version, with fresh chaos and reliable scenario validation.
