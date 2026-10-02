# Historical automatic task admission comparison

> Historical benchmark report. These benchmark harnesses have been retired; current capacity testing uses the [sustained scheduling benchmark](scheduling-capacity-benchmark.md). Reproduction commands below apply to the recorded revisions and archived harnesses, available in the [source snapshot](https://github.com/cloudcarver/anclax/tree/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/pkg/taskcore/e2e). Raw result links point to Git history; generated JSON/log reports are no longer stored in the current tree.

This records the earlier shared-counter implementation at `caf7332`. The final slot implementation, its fresh three-run comparison and its separate 200-iteration chaos run are documented in [Task slot admission measurements](async-task-slot-benchmark.md). References to the PR below mean `caf7332`, not the current PR head.

This compares framework revision `1681140` (v1.4.1) with `caf7332` (PR #71) using the same `admission_benchmark_test.go` in both checkouts. It exercises `BuildWorkerComponents` and automatic polling, including handler execution, renewal and committed finalization. The existing LIMIT 1 load loop and manual `Runtime.RunTask` capacity test do not measure the automatic batch change.

## Method

- Apple M5 / arm64, 16 GiB host RAM; Go 1.26.5 with `GOMAXPROCS=2`, without the race detector for timing.
- PostgreSQL 17.11 in Docker, limited to 2 CPUs / 1 GiB, `pg_stat_statements.track=all` and `track_io_timing=on`. Both revisions use the same local image and database settings; each invocation starts its own database container. Main pool maximum 50, renewal pool maximum 10.
- One Worker with either 100 or 200 execution slots, 20 ms polling, 1 s heartbeat/renewal, 9 s lease TTL, and strict percentage zero. The baseline uses its ordinary per-slot claims; the PR uses its default batch size of 32 and one batch in flight.
- Each case starts from a freshly populated, vacuumed/analyzed queue of 2,000 tasks. Handlers wait 20 ms and return successfully. Four workloads: no tags; shared unlimited global/group tags; the same tags limited to global 32/group 8; and 256 serial queues with 100,000 completed historical rows.
- Measure from Worker start through completion and shutdown, with a 90 s per-case observation limit. Setup, migrations, data loading and `VACUUM ANALYZE` are outside the timed interval. This is a fixed-queue drain test, including connection startup and ramp-down, rather than steady-state or maximum-capacity measurement.
- Three runs of each revision, ordered before/after, after/before, before/after. Cases run sequentially after the 200-iteration chaos suite finishes. Background host services remain running.

Only framework migrations are applied. Application-specific production indexes and business workloads are not recreated. When a Worker stops after a heartbeat/maintenance timeout, the test keeps observing until the case deadline; its low completed/s value includes that stopped interval and must not be used to calculate a speedup multiplier.

Results retain completed/expected tasks, repeated attempts, remaining permits, renewal errors/losses, and transaction SQLSTATE errors. Recovered tag-busy/serialization retries are counted; deadlocks or failure to drain the queue do not count as passing. Renewal query-error counts include cancellations and are distinct from lost leases or repeated handlers. A timed-out baseline is not presented as a successfully completed throughput test.

Intentional shutdown cancellation is excluded from transaction-error acceptance. The PR also emitted ten `conn closed` rollback diagnostics from claim/control transactions around case shutdown. These messages and the baseline's runtime errors are preserved as per-case counts and timestamps in each run's `logErrors`; passing workload acceptance does not assert an empty error log.

Claim/finalize transaction latency includes pool acquisition and commit for transactions that reach a query, measured identically by a model wrapper on both revisions. Pool failures before a query starts appear in runtime logs but cannot be assigned a query name by this wrapper; failed-case percentiles therefore do not describe all admission attempts. Finalize transaction percentiles do not include time between retry attempts. `pg_stat_statements` supplies business-claim calls, server execution time and logical shared-buffer hits/reads; these are not physical disk IOPS. Database CPU is the container's cgroup CPU-time delta around the timed work, including observer/background database activity and a small monitoring margin. Pool acquire duration is cumulative acquisition time, not wall-clock time or exclusively queue waiting.

## Recorded results, 2026-09-15

The PR passed all 24 cases. The baseline passed the six untagged cases; its other 18 cases reached the 90 s observation limit without completing the queue. All individual measurements, including failures and transaction latency distributions, are retained in the [comparison JSON](https://github.com/cloudcarver/anclax/blob/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/docs/benchmarks/task-admission-comparison-2026-09-15.json), together with revisions, image ID, harness SHA-256, execution order and aggregate medians.

Each row represents three repetitions. Successful drain times are medians, with the PR's minimum–maximum in parentheses. Failed baseline rows show the range of tasks completed at the observation cutoff; the expected count is always 2,000.

| Workload | Slots | Baseline result | PR drain time, seconds | PR completed tasks/s, median |
| --- | ---: | --- | ---: | ---: |
| Untagged | 100 | 2.521 s, all completed | 1.021 (0.947–1.055) | 1,960 |
| Untagged | 200 | 2.635 s, all completed | 0.853 (0.775–0.888) | 2,345 |
| Shared unlimited tags | 100 | Timed out; 1,202–1,335 completed | 0.918 (0.870–1.145) | 2,179 |
| Shared unlimited tags | 200 | Timed out; 1,022–1,557 completed | 1.128 (0.853–1.305) | 1,773 |
| Shared limited tags | 100 | Timed out; 1,050–1,292 completed | 16.140 (16.085–16.546) | 124 |
| Shared limited tags | 200 | Timed out; 7–29 completed | 16.399 (16.166–16.475) | 122 |
| Serial queues + 100,000 historical rows | 100 | Timed out; 0–2 completed | 1.038 (0.975–1.065) | 1,927 |
| Serial queues + 100,000 historical rows | 200 | Timed out; 0 completed | 0.884 (0.837–1.088) | 2,263 |

For the healthy untagged comparison, median completed throughput increased **2.47× at 100 slots** and **3.09× at 200 slots**. Median business-claim statement calls fell from 2,270 to 115 and from 2,499 to 69 per queue. Median database CPU time per completed task fell from 2.57 to 1.01 ms and from 2.73 to 0.86 ms, respectively. Shared unlimited-tag cases made **zero counter-update statement calls** on the PR.

The baseline produced two `40P01` finalization errors in the shared-unlimited, 200-slot workload, and two tasks had more than one attempt. A [saved PostgreSQL log excerpt](https://github.com/cloudcarver/anclax/blob/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/docs/benchmarks/task-admission-baseline-deadlock-2026-09-15.log) shows a local `ClaimNormalTaskByGroup` / `FinalizeTaskAttempt` transaction lock cycle involving `release_task_tag_permits` and `task_tag_concurrency`, matching the SQL pair and relation reported in production. This does not reconstruct the original production transaction schedule. The PR recorded no deadlocks, renewal errors/losses, repeated attempts or leftover permits in these 24 cases.

**Finite hot tags still have a measurable cost.** The six PR cases with global limit 32 and group limit 8 recorded 35,942 recovered `55P03` finalization retries across 12,000 tasks, about three per task. All handlers ran once. The roughly 122–124 tasks/s and 16 s drain times are consistent with waiters being woken by maintenance at most once every 250 ms per Worker: a saturated global quota of 32 can yield roughly 128 admissions/s when each wakeup fills one quota's worth of work. This is an inference from the measured rate and current wakeup cadence, not an isolated attribution experiment. Hot-tag retries and wakeup latency remain optimization opportunities; raising execution slots from 100 to 200 did not improve this workload.

## Reproduce

The opt-in test skips unless explicitly enabled:

```sh
GOMAXPROCS=2 ANCLAX_ADMISSION_BENCH=1 \
  ANCLAX_ADMISSION_BENCH_REVISION="$(git rev-parse HEAD)" \
  ANCLAX_ADMISSION_BENCH_TIMEOUT=90 ANCLAX_TEST_REPORT_DIR=/tmp/admission-after \
  go test -tags=smoke ./pkg/taskcore/e2e -run '^TestTaskAdmissionBenchmark$' -count=1 -v -timeout=20m
```

For the baseline, create a detached worktree at `1681140`, copy only `pkg/taskcore/e2e/admission_benchmark_test.go` into it, and run the same command there. Use separate report directories and alternate revisions. The benchmark creates and removes its own named container with a dynamically allocated localhost port. Do not run measured cases concurrently with other load tests.

Overrides: `ANCLAX_ADMISSION_BENCH_TASKS` (2,000), `ANCLAX_ADMISSION_BENCH_HISTORY` (100,000), `ANCLAX_ADMISSION_BENCH_HANDLER_MS` (20), `ANCLAX_ADMISSION_BENCH_CONCURRENCY` (`100,200`), `ANCLAX_ADMISSION_BENCH_SCENARIOS` (all four), and `ANCLAX_SMOKE_POSTGRES_IMAGE` (`postgres:17`). The JSON report is written even when workload acceptance fails.

## Chaos validation

The PR's PostgreSQL 17 container chaos run passed 200 iterations with seed 424242 in 617.24 seconds. It submitted 864 tasks: 860 completed, four cancelled, and none pending/running/failed at the end. Fault injection included 21 Worker disruptions, 13 PostgreSQL restarts and eight control-plane outages. The durable tag audit recorded 588 counter increases with zero limit/permit violations; all permits drained. See the [recorded summary and assertions](https://github.com/cloudcarver/anclax/blob/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/docs/benchmarks/task-admission-chaos-200-2026-09-15.json).

```sh
ANCLAX_TASKCORE_CHAOS_POSTGRES_IMAGE=postgres:17 \
  ANCLAX_TASKCORE_CHAOS_ITERATIONS=200 go test -tags=smoke ./pkg/taskcore/chaos \
  -run '^TestContainerizedTaskcoreChaosSmoke$' -count=1 -v -timeout=60m
```

These results concern synthetic short tasks on the recorded local database. They do not establish a production concurrency limit or replace representative business-load measurements after migration.
