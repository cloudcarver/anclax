# Task admission design

The v1.4.1 production report identified claim/finalize deadlocks involving tag counters and expensive serial-history checks. Execution slots also determined the number of concurrent automatic claim requests. Migration 15 and bounded batch admission address these related paths together.

## Lock protocol

Task transactions first lock their task rows, then try every required limited-tag transaction advisory guard. They never wait for another tag guard. Counter/permit writes require the guard, and a rejected candidate rolls back its own partial acquisitions in a PL/pgSQL subtransaction. Successful candidates retain guards until the batch commits.

Finalization and expiry release permits without waking other task rows while holding tag guards. Indexed maintenance wakes tasks separately, and admission rechecks all limits. Thus counter contention cannot create the former blocking tag-to-task dependency. Advisory hash collisions can cause conservative contention/retries, but cannot admit excess capacity. Raw writes to counters/permits bypassing this protocol are unsupported.

Configuration is rare and uses a different synchronization point: a before-statement trigger acquires an `EXCLUSIVE` task-table lock before any configuration-row lock. It drains task transactions, backfills active attempt snapshots, and publishes the limit atomically. It requires the framework's default `READ COMMITTED` isolation to see task commits that occurred while waiting. Configuration must run in a short dedicated transaction before unrelated row locks. Backfill can delay renewal and scheduling; large installations need to budget for this pause.

Unlimited tags retain membership and a per-attempt `lease_tags` snapshot, with no admission-time shared counter/permit mutation. Editing task attributes does not change the active attempt's tags. Removing a limit clears its permits/count; re-enabling backfills snapshots again, including paused/cancelled and expired leases awaiting recovery. Usage may exceed a newly enabled or lowered limit until existing attempts drain.

## Database admission capacity

Each automatic Worker allows one batch claim in flight and reserves local execution/strict slots before querying. The default maximum batch is 32 tasks, independently of execution concurrency. A partial/empty batch yields; a full batch can continue filling remaining slots. Normal group weights rotate the preferred group per batch, with fallback groups inside the same query. Manual known-ID requests share task capacity but do not use the automatic batch scanner.

SQL bounds strict candidates by their reservation and normal candidates by batch size plus 32. This bounds tag admission work, not all filtering/sorting/index reads. Both legacy and batch SQL explicitly exclude unleased history from active serial checks; new partial indexes support leased rows and pending FIFO heads. Hot finite tags still serialize counter updates and can produce empty batches under contention.

## Finalization

Handler completion retains the worker slot and lease registration through finalization. Each outcome transaction suspends and drains that attempt's renewal to avoid racing its own row update, using the last confirmed lease deadline. A failed, explicitly rolled-back transaction resumes renewal and retries SQLSTATE `40P01`, `40001`, or `55P03` with jitter, within five seconds and the caller's deadline. The handler is not rerun by this retry. Uncertain connection/commit outcomes are not retried as if they were known rollbacks. At-least-once task recovery and external-effect idempotency still apply.

## Verification boundaries

Real PostgreSQL tests cover uncommitted updated counter tuples, rollback of partial guards, concurrent batch/finalize contention, live quota backfill, version fencing, migrations 14→15→14, 100,000 historical serial rows, and a Worker filling 100 slots with four batch queries. A forced 900 ms tag conflict outlasts the test lease's original 600 ms TTL while renewal continues and the handler executes once. Container fault tests cover owner death, database partition, recovery and quota removal/re-enabling.

The exact production deadlock schedule has not been reproduced locally. Deterministic protocol tests establish these tested lock/recovery properties; a short stress pass and synthetic query counts do not establish production capacity. Production must be re-baselined after upgrade. Old/new protocol versions must not run together during migration or rollback; see [upgrade instructions](async-task-tag-concurrency.md#storage-compatibility-and-upgrade).
