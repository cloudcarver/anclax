# Ready-state task prefetch

Migration 16 separates business admission from Worker execution. The `tasks` table is also the durable ready queue; there is no second queue-membership table. `ready` means all finite tag slots and serial exclusion have been reserved, but no Worker owns the execution yet. `running` identifies automatic business execution and system execution. Legacy/manual pending claims remain supported with their existing lease representation.

## State and ownership

| Phase | Resources | Worker lease | Attempts |
| --- | --- | --- | --- |
| pending | None, except an older fenced attempt awaiting recovery | None, or an older attempt awaiting recovery | Unchanged |
| ready | All limited tags, plus serial ownership | None; durable precomputed admission | Unchanged |
| running | The same reserved resources | Bound to the consuming Worker | Incremented once |

Admission locks the candidate, atomically allocates every required independent tag slot, records the admission-time tag snapshot and version, and changes it to ready. Failure leaves it pending without partial allocation. Worker claim locks ready rows, checks routing/strict allowance, and adopts the existing version and resources in one transaction. It does not repeat tag allocation or serial-history checks. Manual by-ID execution can adopt a ready reservation through the same ownership rules.

Ready has no expiry and no fixed quantity limit. Time passing, a stopped scheduler or a disconnected Worker does not invalidate already computed admission. Cancellation, pause, or edits to scheduling attributes revoke an unconsumed reservation under its task row lock. Running attempts retain their execution leases and original tag snapshots through attribute changes and pause/resume fencing. Finalization and renewal retain the independent-slot protocol introduced in migration 15.

Ready owners participate in serial exclusion and FIFO checks. Enabling a quota backfills ready owners as well as outstanding execution leases. Reducing a quota retains existing reservations, including retired overflow, just as it retains existing executions. Unlimited tags do not allocate slots or counters.

## System execution

The framework bootstraps a durable `prefetchTasks` task with unique tag `anclax:system:prefetch`. It uses the reserved control/system lane and claims directly from pending or expired running system tasks. It can start with an empty ready set or exhausted business quotas. Every scheduling round checks the system task's owner, lease version, state and deadline while holding its row lock. Repeating or resubmitting another task of this type does not create another scheduler.

An admission round performs bounded execution-lease recovery and admits at most 256 tasks. This is a transaction/candidate bound, not a ready capacity limit. The system task is long-lived and runs successive bounded transactions under one execution lease. The scheduler observes consumption and ready stock per resource/routing/priority-class group, and can stop admission SQL entirely while a group has sufficient reserve. All waits remain outside transactions. The normal lease keeper renews it while busy or idle. There is no per-round claim/finalize cycle and no new notification mechanism. The integrated Worker reserves two control slots, so the singleton scheduler leaves capacity for cancellation, pause and configuration commands.

## Consumption-paced precomputation

The admission SQL has no global 4,096, Worker-capacity, or strict-ready-count limit. All precomputed tasks remain ready until claimed or explicitly invalidated. Each transaction still admits at most 256 tasks, and PostgreSQL remains authoritative for finite resource allocation, serial ownership and execution fencing. Strict percentages limit Worker execution, not the number of precomputed strict tasks.

The long scheduler observes indexed ready counts and due pending work separately for each resource/routing/priority-class group. Due eligibility excludes routes without a compatible live Worker and groups with a zero quota; later Worker/configuration changes are observed even while admission is paused. Observed stock departures, adjusted for our own committed admissions, estimate consumption without a claim-side counter write. Cancellation and edits can also remove ready rows, conservatively overestimating demand. A separate local forecast projects remaining stock between observations; its predicted departures are never fed back into measured consumption. Neither estimate grants resources. Removed/empty groups are discarded, and takeover reconstructs controller state from PostgreSQL.

Consumption controls normal refill timing, batch demand and whether to pause. The scheduler targets 500 ms of observed consumption, refilling below a 250 ms low watermark (both at least one task). A refill also requires a deficit of at least one whole task, so fractional forecast depletion cannot repeatedly request an extra task from a full small quota. This separation avoids tiny repeated refills around one threshold. There is no fixed 256-task minimum reserve. An empty group without consumption gets a bootstrap request of up to 256; later batch sizes follow the reserve deficit, also bounded to 256. Ready stock itself remains uncapped. Rates rise immediately and decay exponentially with a one-second time constant. Forecast depletion can schedule a refill before the next observation. A stocked group with no consumption pauses candidate SQL, while empty due groups retain bootstrap/recovery attempts.

Empty output only adds an independent retry deadline: 5, 10, 20, 40, 80, then at most 100 ms after each zero-result transaction completes. Any positive output clears this backoff. Output does not reset consumption estimates or decide that demand has stopped; advisory SQL wait reasons remain available as diagnostics. The next attempt must satisfy both the normal demand schedule and the retry deadline. This also recovers when ready is empty and a running owner releases quota, even though no further ready consumption occurs.

Observations target 50 ms for groups with measured consumption and up to 100 ms when idle. After initial admission without a known consumption rate, probes start at 5 ms and back off toward 100 ms until consumption is observed, preventing small quotas from waiting a full idle interval at startup. The maintenance deadline can advance an observation, but observations cannot bypass admission backoff. All waits remain outside transactions. These intervals are policy targets, not completion-latency guarantees. No notification mechanism is introduced.

A separate fenced observation path performs bounded execution-lease recovery and unclassified-group maintenance at least every 250 ms under normal scheduling. Thus paused admission does not suspend recovery, discoverability of new groups or the normal keeper's renewal of the system task. It never recovers ready rows. An ordered lateral LIMIT 1 probe retains the group/due index access path instead of allowing EXISTS to choose a full pending-table scan. Group/due and ready/group indexes support observations without scanning completed history or future pending tasks; counting a deep ready group still has a database cost and must be measured, as must large group catalogs.

`PrefetchTaskSupply` returns the prepared total, advisory wait reason and per-group counts. The original integer `PrefetchReadyTasks` query remains for direct callers; both now omit the removed ready TTL argument. The integrated scheduler also requires `InspectTaskPrefetch`. Observations and estimates do not modify Worker claims or task ownership.

Cancellation, a database error or lost scheduler ownership ends the loop. Graceful Worker shutdown defers the durable task back to pending without accumulating an attempt; an expired owner can be taken over through the ordinary system claim path. Every batch checks the durable fence, so even a caller without a lease keeper stops after losing ownership. System claim orders by the next due time, then ID.

System tasks retain time eligibility, labels, execution leases, renewal, retries and cancellation. They bypass business tag quotas, serial scheduling and weighted admission. TaskStore rejects nonempty tags, serial keys and non-default weights for system tasks. The `prefetchTasks` type and `anclax:system:` unique-tag prefix cannot be enqueued through TaskStore; bootstrap is framework-owned. System task types are framework-owned; high business priority does not confer system status.

## Candidate selection and bounds

`task_admission_groups` is a catalog of normalized **limited** tag sets, required label sets and normal/strict priority class. Task insertion/attribute changes assign a group using indexed lookup; ordinary lifecycle operations do not update a shared group counter. New group creation uses a nonblocking creation guard: concurrent insertions that cannot acquire it commit with an unclassified membership, and the system task classifies up to 256 such rows through a partial index. This prevents opposite multi-task insertion orders from creating unique-key lock cycles. Distinct unlimited tag sets share a group when their routing labels match. The existing configuration barrier reclassifies active tasks when limits are enabled or removed.

The scheduler computes available slot counts once, skips full resource groups, and reads a bounded candidate prefix through each group's pending index. Candidate limits also use the smallest currently available tag capacity. The allocator remains authoritative when the snapshot becomes stale. This avoids revisiting every task in a full group on each Worker claim. The sorted candidate prefix is bounded before row locking; a locking lateral lookup forces per-candidate primary-key access instead of allowing a full-table join scan. A row-version check rejects candidates edited concurrently before their lock is acquired.

Within that ordered prefix, fresh tasks with no limited tags, no serial key and no old execution lease are admitted by one bulk UPDATE. They retain reservation versions and admission-time tag snapshots, including unlimited tags needed for later quota activation. Constrained tasks and expired owners still use the full allocator; an expired owner may need to release slots from its previous snapshot even when its current tags are unlimited. Admission requires READ COMMITTED, and the task-table configuration barrier prevents quota changes during either path. The durable group cursor is written only when its value changes.

Strict priorities retain creation order for ties. Normal scheduling uses the existing sorted weighted-group wheel, with a durable cursor on the singleton system task; weights do not require expanding a large wheel in SQL. Worker batches retain group preference/fallback and strict-cap enforcement. Ready strict admission requires a compatible Worker with a positive strict percentage; the rounded execution allowance remains enforced by Worker claim.

Ready stock has no fixed capacity bound. Registered online Worker routing and heartbeat freshness constrain candidate eligibility; registration capacity now determines whether a Worker can execute business tasks, not a stock ceiling. Ready reservations can occupy quota while waiting for a compatible Worker; this is a real tradeoff of pre-admission. Runtime configuration acknowledgments update the scheduler's strict-cap metadata.

The system admission function disables JIT locally. Its bounded lateral query incurred disproportionate compilation time in the initial prototype; the setting does not change other database queries. Large numbers of distinct resource/routing/priority-class groups, serial-blocked candidates within an available group, and empty catalog groups can still add scheduling work. The global scheduler's own throughput is also a capacity limit and must be measured independently of Worker capacity.

## Upgrade and integration

Stop old Workers, apply migration 16, then start only the new Workers. Do not mix old and new protocols. Migration 16 adds the ready indexes/catalog and backfills active group membership; this requires a maintenance window on large task tables. Migrations 14 and 15 remain unchanged.

Rollback requires stopping Workers first. The down migration returns ready reservations to pending, releases their slots, maps running execution back to the legacy pending lease representation, removes the internal scheduler task and restores migration 15 functions. Existing execution leases and original task attributes remain intact.

Use `BuildWorkerComponents` / `NewWorkerFromConfig` for the integrated scheduler lifecycle. Custom models must implement the generated `PrefetchTaskSupply` and `InspectTaskPrefetch` queries. Custom runtimes must register their capacity with `ConfigureWorkerPrefetch`, bootstrap `EnsureTaskPrefetch`, and reserve system execution capacity for both the long-lived scheduler and other control commands; direct calls to the ready-only batch query do not perform admission themselves. Legacy/manual pending claim APIs retain full admission for compatibility.

Operationally, report ready depth/age separately from pending and executing tasks. Tag `InUse` includes ready reservations and outstanding executions, including expired owners awaiting recovery. The scheduler duration/error metrics include `operation="prefetch"` for each batch transaction and `operation="prefetch_probe"` for observations, independently of the long-lived task's claim/finalize lifecycle. Track total PostgreSQL CPU/SQL work, successful completions, running-lease recovery and group cardinality alongside the smaller Worker claim latency.

## Verification

PostgreSQL regressions cover reservation transfer without a second attempt/allocation, durable ready beyond the old expiry, competing claims, cancel/pause/attribute edits, quota activation and overflow, serial ownership, weighted progress under one shared slot, stale scheduler fencing and unlimited-tag group normalization. Existing lifecycle, migration, renewal and concurrent batch tests also run against the new protocol. Benchmark evidence must include enqueue/group-maintenance cost and system scheduler work, not only Worker claim time.

Long-task regressions verify repeated work under one attempt, idle renewal beyond the lease TTL, simultaneous control progress, shutdown and fencing. A statement-level audit verifies bulk admission, mixed constrained candidates, snapshots and quota backfill. An expired-lease regression exhausts the maintenance prefix before verifying that the full allocator releases the old reservation.

The [initial benchmark](ready-task-prefetch-benchmark.md) records the backlog benefits and unblocked-workload regressions of one-round prefetch. The [long-task follow-up](long-task-prefetch-benchmark.md) compares the loop and bulk-admission changes with that version, with fresh chaos and reliable scenario validation.

The [consumption-paced follow-up](consumption-prefetch-benchmark.md) measures reduced idle SQL/CPU, its arrival-latency cost, and mixed busy throughput against the long-task version.

The historical [supply-first follow-up](supply-prefetch-benchmark.md) compares the former bounded/expiring policy with consumption pacing and the original long-task loop. It does not measure the current durable-ready policy.

The [durable-ready follow-up](durable-ready-prefetch-benchmark.md) measures the current policy against the former supply-first version, including three interleaved repetitions, a matched longer finite-quota run, idle observation cost and fresh 200-round chaos validation. Throughput remains mixed; removing expiry and a fixed stock cap does not establish a general performance improvement.
