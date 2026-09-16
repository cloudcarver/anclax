# Long-lived task prefetch benchmark (2026-09-15)

> Historical benchmark report. These benchmark harnesses have been retired; current capacity testing uses the [sustained scheduling benchmark](scheduling-capacity-benchmark.md). Reproduction commands below apply to the recorded revisions and archived harnesses, available in the [source snapshot](https://github.com/cloudcarver/anclax/tree/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/pkg/taskcore/e2e). Raw result links point to Git history; generated JSON/log reports are no longer stored in the current tree.

Compare one-round ready prefetch (`eafef95`) with long-lived prefetch and bulk simple-task admission (`8b8a943`, production sources `300a850`). Both revisions already use migration 16, independent tag slots, admission groups and the bounded candidate query. This is a fresh comparison of the follow-up, not a reuse of the earlier main-versus-ready measurements.

**The follow-up does not demonstrate an overall performance gain.** Eleven of fourteen scenario/concurrency combinations have lower throughput medians, by 2.1–12.2%. The long-lived task eliminates per-round finalization, but prefetch SQL calls increase. PostgreSQL CPU does not consistently improve. Keep the PR in draft while reviewing those costs.

## Change and method

- Keep one scheduler execution lease across bounded transactions, with ordinary renewal and a 20 ms idle poll outside transactions. The integrated Worker reserves two control slots so other system commands can progress. No event-driven wakeup was added.
- Collect fresh candidates without finite tags, serial keys or old leases and update their ready reservations in one statement. Constrained tasks and expired owners use the full allocator. Preserve tag snapshots for later quota backfill; skip unchanged cursor writes.
- Identical automatic-Worker harness SHA-256 `c20bb2d6541904549948b10987917684452342c6051d8ccf8f5c5af28a3c6fd3`; seven scenarios, 100/200 execution slots, 2,000 runnable tasks per case, 20 ms handlers, main pool 50, renewal pool 10, claim batch 32 and prefetch batch 256.
- Apple M5 / 16 GiB, Go 1.26.5 arm64, PostgreSQL 17.11 Docker / 2 CPU / 1 GiB, `GOMAXPROCS=2`. Finite shared quota is 32 globally plus eight groups of 8. Serial history has 100,000 completed rows. Blocked cases have 0/20,000/200,000 higher-weight tasks behind a full tag.
- Three alternating pairs, before/after, after/before, before/after. No chaos, builds or stress tests overlap timed runs; the existing unrelated background container remains. Values are medians of three runs, and latency columns are medians of per-run percentiles. Raw runs and ranges are retained.

## Execution results

| Scenario | Slots | One-round tasks/s | Long-task tasks/s | Change | PG CPU seconds before → after | Claim p95 ms before → after |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| untagged | 100 | 1,840.5 | 1,892.3 | +2.8% | 2.126 → 2.127 | 14.96 → 16.33 |
| untagged | 200 | 2,200.3 | 2,133.3 | -3.0% | 1.837 → 1.844 | 46.49 → 50.78 |
| unlimited_shared | 100 | 2,142.9 | 2,013.6 | -6.0% | 1.855 → 1.936 | 15.58 → 16.74 |
| unlimited_shared | 200 | 2,178.4 | 2,128.0 | -2.3% | 1.824 → 1.846 | 53.06 → 54.16 |
| limited_shared | 100 | 543.3 | 593.2 | +9.2% | 2.000 → 2.162 | 2.57 → 2.65 |
| limited_shared | 200 | 606.2 | 593.5 | -2.1% | 2.172 → 2.087 | 2.84 → 2.67 |
| serial_history | 100 | 1,636.9 | 1,498.9 | -8.4% | 2.419 → 2.580 | 30.92 → 29.66 |
| serial_history | 200 | 1,893.1 | 1,661.4 | -12.2% | 2.059 → 2.388 | 37.40 → 56.70 |
| blocked_0 | 100 | 1,927.1 | 1,869.2 | -3.0% | 2.074 → 2.083 | 15.52 → 26.38 |
| blocked_0 | 200 | 2,093.3 | 2,017.6 | -3.6% | 1.947 → 1.988 | 47.29 → 62.52 |
| blocked_20000 | 100 | 2,021.5 | 1,871.7 | -7.4% | 1.946 → 2.088 | 17.45 → 20.49 |
| blocked_20000 | 200 | 2,236.7 | 2,003.1 | -10.4% | 1.823 → 1.959 | 40.17 → 57.04 |
| blocked_200000 | 100 | 1,309.1 | 1,512.6 | +15.5% | 3.017 → 2.544 | 19.88 → 28.46 |
| blocked_200000 | 200 | 1,811.0 | 1,619.2 | -10.6% | 2.267 → 2.502 | 53.01 → 66.34 |

## Admission and lifecycle work

| Scenario | Slots | Prefetch calls before → after | Finalize calls before → after | Empty Worker claims before → after |
| --- | ---: | ---: | ---: | ---: |
| untagged | 100 | 43 → 73 | 2043 → 2001 | 18 → 14 |
| untagged | 200 | 19 → 58 | 2019 → 2001 | 20 → 6 |
| limited_shared | 100 | 125 → 189 | 2125 → 2001 | 371 → 270 |
| limited_shared | 200 | 127 → 190 | 2127 → 2001 | 357 → 267 |
| serial_history | 100 | 44 → 55 | 2044 → 2001 | 24 → 28 |
| serial_history | 200 | 24 → 30 | 2024 → 2001 | 38 → 12 |

Prefetch calls are bounded batch transactions, not scheduler attempts. The after version performs them under one long-lived attempt. Finalize totals include business tasks and any system lifecycle outcomes captured before shutdown. `PrefetchReadyTasks.Rows` counts returned result rows, not prepared tasks.

At 100 slots, the finite-quota case goes from 125 to 189 prefetch calls while finalizations fall from 2,125 to 2,001. Untagged work goes from 43 to 73 prefetch calls. The loop immediately recomputes after a productive round; that next round can find a full ready window or exhausted quota and still incurs a database transaction and recovery work. This is a concrete remaining source of extra scheduling work, but the benchmark does not isolate its CPU contribution from control polling, row updates or transaction contention.

Baseline variance is substantial in the finite-quota case: 538–853 tasks/s at 100 slots and 539–819 at 200, versus 592–600 and 577–609 after the change. The 100-slot median increase is therefore not evidence of a stable gain. The serial-history 200-slot runs have non-overlapping observed ranges (1,832–1,989 before, 1,584–1,699 after), warranting attention to that regression. These three-run ranges are not confidence intervals.

## Including insertion

| Scenario | Slots | Enqueue seconds before → after | Enqueue + execution before → after | Combined speedup | Combined PG CPU before → after |
| --- | ---: | ---: | ---: | ---: | ---: |
| untagged | 100 | 0.148 → 0.171 | 1.234 → 1.197 | 1.03× | 2.169 → 2.178 |
| untagged | 200 | 0.141 → 0.154 | 1.047 → 1.082 | 0.97× | 1.883 → 1.892 |
| unlimited_shared | 100 | 0.155 → 0.161 | 1.084 → 1.145 | 0.95× | 1.919 → 2.005 |
| unlimited_shared | 200 | 0.168 → 0.147 | 1.087 → 1.084 | 1.00× | 1.887 → 1.910 |
| limited_shared | 100 | 0.173 → 0.169 | 3.855 → 3.536 | 1.09× | 2.066 → 2.232 |
| limited_shared | 200 | 0.179 → 0.187 | 3.478 → 3.542 | 0.98× | 2.242 → 2.170 |
| serial_history | 100 | 0.176 → 0.166 | 1.402 → 1.498 | 0.94× | 2.477 → 2.644 |
| serial_history | 200 | 0.170 → 0.163 | 1.226 → 1.367 | 0.90× | 2.111 → 2.447 |
| blocked_0 | 100 | 0.168 → 0.157 | 1.206 → 1.227 | 0.98× | 2.131 → 2.146 |
| blocked_0 | 200 | 0.140 → 0.152 | 1.096 → 1.135 | 0.97× | 1.998 → 2.039 |
| blocked_20000 | 100 | 0.715 → 0.709 | 1.744 → 1.778 | 0.98× | 2.602 → 2.662 |
| blocked_20000 | 200 | 0.711 → 0.716 | 1.598 → 1.744 | 0.92× | 2.422 → 2.588 |
| blocked_200000 | 100 | 6.011 → 5.929 | 7.483 → 7.263 | 1.03× | 8.647 → 8.260 |
| blocked_200000 | 200 | 5.802 → 5.880 | 6.915 → 7.135 | 0.97× | 7.751 → 8.030 |

Insertion includes rule setup and blocked/runnable inserts with their grouping triggers; it excludes migration, serial-history setup, Worker construction and VACUUM. Docker CPU-observer round trips add fixed wall-clock overhead. PostgreSQL CPU during execution includes scheduler admission, claims, finalization and observation. SQL elapsed time uses top-level pg_stat_statements and is not CPU time. PoolWaitSeconds is cumulative pgx AcquireDuration, not pure wait time.

## Correctness and limitations

Production commit `300a850` passed **200 chaos rounds** in 606.9 s: 864 submitted, 860 completed, 4 cancelled, zero pending/ready/running tasks left. Faults included 21 Worker disruptions, 13 PostgreSQL restarts and 8 control-plane outages, with 103 runtime-config updates. The durable tag audit passed with zero violations and all permits drained.

PostgreSQL 15/17 race regressions cover long-task idle renewal, control progress, shutdown, fencing, one-statement bulk admission, quota backfill, expired old-owner release, strict/weighted behavior, serial exclusion and migrations. The full race unit suite passed.

During this follow-up, generated DST script assertions were found to recover into an unnamed return value and incorrectly return nil. The generator now propagates fatal/nonfatal assertions and panics; an executable generated-code regression verifies all three. Two execution-state expectations changed from pending to running, and drain assertions now include ready/running business tasks while excluding the durable scheduler. The corrected entire scenario suite passed three repeats. The earlier reported DST success is historical process output, not reliable evidence that every script assertion passed; direct Go assertions in PostgreSQL, chaos and benchmark tests were unaffected.

All 84 timed cases passed: 168,000 business completions and attempts, zero repeated tasks, lease errors/loss, remaining slots or recorded claim/prefetch/finalize SQLSTATE errors. Shutdown cancellation emitted rollback-on-closed-connection logs in both revisions (11 before, 17 after); these are retained in the metadata. Passing does not mean every log line is error-free.

These are finite, warmed synthetic workloads on one host with three repeats, not production capacity or confidence intervals. The comparison measures both requested optimizations together, including the extra control slot and skipped cursor writes; it does not isolate each contribution. Ready still adds a state update, reservation time, grouping and periodic refill costs. High group cardinality, heterogeneous routing, lower-rate arrival latency and completely idle polling cost are not measured here.

The [initial main-versus-ready report](ready-task-prefetch-benchmark.md) remains historical evidence of the blocked-backlog benefit. Do not combine ratios across separate runs into a fresh comparison with main. Migration 16 still requires stopped Workers for upgrade/rollback; [integration and recovery](ready-task-prefetch.md).

## Reproduction and artifacts

Use the [same build and run commands](ready-task-prefetch-benchmark.md#reproduction-and-evidence), with detached worktrees at `eafef95` and `8b8a943`, separate output directories and the six-run order above. The harness is already identical in both worktrees.

```sh
ANCLAX_TASKCORE_CHAOS_POSTGRES_IMAGE=postgres:17 ANCLAX_TASKCORE_CHAOS_ITERATIONS=200 \
go test -tags=smoke ./pkg/taskcore/chaos -run '^TestContainerizedTaskcoreChaosSmoke$' -count=1 -v -timeout=30m
```

- [Every raw benchmark run, environment, hashes and medians](https://github.com/cloudcarver/anclax/blob/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/docs/benchmarks/task-long-prefetch-comparison-2026-09-15.json)
- [Fresh 200-round chaos and allocation audit](https://github.com/cloudcarver/anclax/blob/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/docs/benchmarks/task-long-prefetch-chaos-200-2026-09-15.json)
- [Validation commands, results and log hashes](https://github.com/cloudcarver/anclax/blob/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/docs/benchmarks/task-long-prefetch-validation-2026-09-15.json)
