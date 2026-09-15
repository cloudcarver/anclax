# Ready prefetch benchmark (2026-09-15)

Compare merged independent-slot admission (`0575291`, PR #71) with ready-state prefetch (`b3bf02c`). Both use exactly the same automatic-Worker benchmark harness. This measures the complete change, including resource grouping, ready transfer, bounded candidate lookup and the system function's local JIT setting.

The design substantially reduces full-tag backlog scanning, with a measurable cost in some unblocked workloads. The full results below include that regression and insertion overhead.

## Method

- Apple M5 / 16 GiB, Go 1.26.5 arm64; PostgreSQL 17.11 in Docker with 2 CPU / 1 GiB.
- `GOMAXPROCS=2`, 100 or 200 execution slots, main pool 50, independent renewal pool 10, claim batch 32, handler delay 20 ms.
- Each case completes 2,000 runnable business tasks. `limited_shared` uses global quota 32 plus eight group quotas of 8. `serial_history` adds 100,000 completed rows across 256 serial keys.
- Blocked cases insert 0 / 20,000 / 200,000 higher-weight tasks behind a tag with quota zero, followed by the same 2,000 runnable tasks.
- Three runs per revision, order before/after, after/before, before/after. Cases run sequentially in fresh scenario state, with `VACUUM ANALYZE` before timing. No chaos, builds or stress tests run concurrently; an unrelated pre-existing background container remains unchanged.
- Tables report medians of three runs. Latencies are medians of per-run percentiles, not pooled percentiles; the JSON retains every run and throughput ranges.

## Results

| Scenario | Slots | Before tasks/s | Ready tasks/s | Ratio | PG CPU seconds before → ready | Claim p95 ms before → ready |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| untagged | 100 | 1,990.6 | 1,793.0 | 0.90× | 2.051 → 2.201 | 17.59 → 24.96 |
| untagged | 200 | 2,063.7 | 2,138.8 | 1.04× | 1.959 → 1.902 | 50.34 → 48.45 |
| unlimited_shared | 100 | 2,021.6 | 2,049.4 | 1.01× | 1.955 → 1.925 | 19.25 → 19.46 |
| unlimited_shared | 200 | 1,777.4 | 1,946.4 | 1.10× | 2.109 → 2.094 | 59.91 → 53.51 |
| limited_shared | 100 | 961.3 | 824.4 | 0.86× | 2.002 → 2.121 | 9.96 → 2.83 |
| limited_shared | 200 | 962.9 | 828.6 | 0.86× | 1.895 → 1.960 | 9.01 → 2.89 |
| serial_history | 100 | 1,923.0 | 1,712.5 | 0.89× | 2.063 → 2.368 | 28.01 → 28.52 |
| serial_history | 200 | 1,800.4 | 1,876.0 | 1.04× | 2.242 → 2.103 | 45.42 → 37.89 |
| blocked_0 | 100 | 1,877.2 | 1,816.0 | 0.97× | 2.157 → 2.196 | 23.73 → 22.30 |
| blocked_0 | 200 | 2,014.7 | 1,825.3 | 0.91× | 2.015 → 2.196 | 41.90 → 59.99 |
| blocked_20000 | 100 | 327.0 | 1,853.1 | 5.67× | 11.324 → 2.154 | 130.55 → 25.00 |
| blocked_20000 | 200 | 338.9 | 1,873.8 | 5.53× | 10.877 → 2.168 | 124.78 → 51.69 |
| blocked_200000 | 100 | 35.8 | 1,301.5 | 36.36× | 108.833 → 3.141 | 1072.30 → 29.58 |
| blocked_200000 | 200 | 31.5 | 1,632.3 | 51.76× | 124.822 → 2.405 | 1129.92 → 56.18 |

PostgreSQL CPU includes system admission and finalization, not just Worker claim. SQL totals use top-level `pg_stat_statements` to avoid counting nested statements twice. The raw report also includes p50/p95/p99, pool statistics, attempts, lease health, SQLSTATE errors and remaining permits. `PrefetchReadyTasks.Rows` counts result rows, not prepared tasks; throughput always counts completed business tasks.

## Cost moved to enqueue

Ready prefetch adds group classification at insertion and another task-state write during execution. The following comparison includes both measured insertion and Worker execution:

| Scenario | Slots | Enqueue seconds before → ready | Enqueue + execution seconds before → ready | Combined speedup | Combined PG CPU seconds before → ready |
| --- | ---: | ---: | ---: | ---: | ---: |
| untagged | 100 | 0.130 → 0.160 | 1.124 → 1.278 | 0.88× | 2.081 → 2.248 |
| untagged | 200 | 0.118 → 0.155 | 1.087 → 1.084 | 1.00× | 1.982 → 1.948 |
| limited_shared | 100 | 0.145 → 0.167 | 2.223 → 2.585 | 0.86× | 2.046 → 2.193 |
| limited_shared | 200 | 0.151 → 0.180 | 2.229 → 2.587 | 0.86× | 1.934 → 2.031 |
| blocked_20000 | 100 | 0.464 → 0.781 | 6.571 → 1.854 | 3.54× | 11.652 → 2.740 |
| blocked_20000 | 200 | 0.488 → 0.724 | 6.421 → 1.779 | 3.61× | 11.246 → 2.783 |
| blocked_200000 | 100 | 3.495 → 5.979 | 59.420 → 7.456 | 7.97× | 112.041 → 8.721 |
| blocked_200000 | 200 | 3.428 → 6.037 | 67.135 → 7.526 | 8.92× | 128.038 → 8.484 |

Insertion timing includes the blocked rule setup and every blocked/runnable row, including grouping triggers and Docker CPU-observer round trips. It excludes schema migration, serial-history preparation, Worker construction and VACUUM. These sums therefore describe enqueue plus execution, not complete database setup time. Large task-table migration/index-building cost is outside this benchmark.

## Query work and correctness

The candidate-plan regression extracts the production selection query. With 0 / 20,000 / 200,000 full-quota rows it returns 256 candidates, and each task-table scan/lookup examines at most 512 rows including repeated loops. The final locking lookup uses the primary key in both tested PostgreSQL versions, avoiding the complete task-table join scan seen in the initial prototype. Timings in this separate plan artifact were collected during correctness tests and are not isolated benchmark timings.

Final commit `b3bf02c` passed **200 chaos rounds** in 599.5 s: 864 submitted, 860 completed, 4 cancelled, and no pending/ready/running tasks left. Faults included 21 Worker disruptions, 13 PostgreSQL restarts, 8 control-plane outages and 103 runtime-config updates. The durable tag audit recorded 589 observations, zero violations, all permits drained and no terminal tag memberships left.

Final Go sources passed the full race unit suite and three complete task-scenario repetitions. PostgreSQL 15/17 regressions cover resource transfer, expiry versus claim, scheduling edits, tag-limit backfill/shrink, weighted progress, serial exclusion, stale scheduler versions, concurrent group creation and migration down/up with live ready/running tasks. Offline Worker command cleanup includes both pending and running states.

All **84 cases** completed their 2,000 business tasks: 168,000 completions and attempts, no repeated attempts, lease errors/loss or remaining permits, and no recorded claim/prefetch/finalize SQLSTATE errors. Shutdown cancellation emitted rollback-on-closed-connection logs in both revisions (38 before, 13 after); these are preserved in the comparison metadata. Passing does not mean the logs contain no errors.

## Limits and rollout

The saturated-backlog benefit is substantial: roughly 5.5–5.7× Worker-stage throughput at 20,000 blocked rows and 36–52× at 200,000. Including insertion reduces these gains to 3.5–3.6× and 8–9× respectively. Enqueuing 200,000 blocked rows plus the runnable batch takes about 6 seconds after the change versus 3.4–3.5 seconds before it.

This is not an across-the-board throughput improvement. The finite shared-quota case loses about 14% at both concurrency levels, despite lower claim p95; untagged and serial-history cases at 100 slots lose about 10–11%. Other unblocked results vary in both directions. Ready admission adds work and occupies quota before execution, so smaller claim latency alone does not establish higher useful throughput. These are short synthetic runs with 20 ms handlers, and three repetitions do not establish confidence intervals or production capacity.

A ready reservation consumes quota before execution, expires after two seconds and is recovered by the independent system lane. The global ready window is separate from running capacity and capped at 4,096. This benchmark does not measure many distinct tag/routing groups or heterogeneous Worker routing; those can still increase scheduler work or reserve capacity ahead of a compatible Worker. It does not establish production limits at 400/1,000/2,000 Workers.

Stop old Workers before applying migration 16, then start only new Workers. Do not mix protocols. Rollback also requires stopped Workers; it releases unconsumed ready reservations and restores legacy pending execution leases. See [the design and integration contract](ready-task-prefetch.md).

## Reproduction and evidence

Create detached worktrees at the two revisions, copy `pkg/taskcore/e2e/admission_benchmark_test.go` from the after worktree to the before worktree, then build each with:

```sh
GOMAXPROCS=2 go test -c -tags=smoke ./pkg/taskcore/e2e -o /tmp/anclax-ready-VERSION.test
```

Run each binary with a fresh report directory, following the six-run order above:

```sh
GOMAXPROCS=2 ANCLAX_ADMISSION_BENCH=1 \
ANCLAX_SMOKE_POSTGRES_IMAGE=postgres:17 \
ANCLAX_ADMISSION_BENCH_CONCURRENCY=100,200 \
ANCLAX_ADMISSION_BENCH_TASKS=2000 ANCLAX_ADMISSION_BENCH_HISTORY=100000 \
ANCLAX_ADMISSION_BENCH_HANDLER_MS=20 ANCLAX_ADMISSION_BENCH_TIMEOUT=120 \
ANCLAX_ADMISSION_BENCH_SCENARIOS=untagged,unlimited_shared,limited_shared,serial_history,blocked_0,blocked_20000,blocked_200000 \
ANCLAX_ADMISSION_BENCH_REVISION=COMMIT ANCLAX_TEST_REPORT_DIR=/tmp/REPORT \
/tmp/anclax-ready-VERSION.test -test.run='^TestTaskAdmissionBenchmark$' -test.v -test.timeout=20m
```

- [Full comparison, revisions, hashes and measurements](benchmarks/task-ready-prefetch-comparison-2026-09-15.json)
- [PostgreSQL candidate plans](benchmarks/task-ready-prefetch-candidate-plans-2026-09-15.json)
- [Final 200-round chaos](benchmarks/task-ready-prefetch-chaos-200-2026-09-15.json)
- [Validation commands and log hashes](benchmarks/task-ready-prefetch-validation-2026-09-15.json)
