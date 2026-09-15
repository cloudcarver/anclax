# Task slot admission measurements

This records the final slot protocol at `a2238006f7e3566e7745831de85c1f2a8cfef775`, compared with the earlier shared-counter implementation at `caf73329c7fae49cc4259164e0c001398a70ee6a`. Both are revisions of PR #71. Unlike the [historical v1.4.1 comparison](async-task-admission-benchmark.md), both already have automatic batch claims, unlimited-tag accounting removal and the serial-history indexes. This comparison isolates the subsequent implementation change as a whole; it does not isolate every SQL change individually.

## Method

The same opt-in `admission_benchmark_test.go` was used byte-for-byte in both builds (SHA-256 `c3c7372cd3ca968d3e92f226393a2139a045bbaa84d57dff8f03a33942578a2a`). The source was committed before the final benchmark build. Exact revisions, binary hashes, image ID, timestamps, all measurements and log-error counts are in the [comparison JSON](benchmarks/task-slot-admission-comparison-2026-09-15.json).

- Apple M5 / arm64, 16 GiB host RAM; Go 1.26.5, `GOMAXPROCS=2`, without the race detector for timing.
- PostgreSQL 17.11 in Docker with 2 CPU / 1 GiB limits, `pg_stat_statements.track=all` and `track_io_timing=on`. A new database container per invocation, main pool maximum 50 and renewal pool maximum 10.
- One automatic Worker, 100 or 200 execution slots, batch maximum 32, one batch query in flight, 20 ms polling, 1 s heartbeat/renewal, 9 s lease TTL and strict percentage zero.
- Each case drains a freshly populated and vacuumed/analyzed queue of 2,000 tasks. Handlers wait 20 ms and complete. Limited tags have global capacity 32 and eight group tags with capacity 8 each. Serial cases use 256 queues and 100,000 completed historical rows.
- Three sequential repetitions per revision, ordered counter-1, slots-1, slots-2, counter-2, counter-3, slots-3. Chaos and load tests finished before these measurements; ordinary background host services remained running.

Timing starts at Worker startup and ends after drain and shutdown, excluding setup, migrations, queue loading and vacuum. These are short fixed-queue drain tests, including startup and ramp-down, with a 90 s case limit. They are not steady-state, production business workloads or maximum-capacity measurements. Only framework migrations are applied, without application-specific indexes. Three repetitions describe observed variation, not statistical confidence intervals.

Transaction latency includes pool acquisition and commit for transactions reaching a query. Failures before a query are retained in runtime logs and cannot be assigned a query name by the wrapper. Finalize percentiles exclude time between retries. `PoolWaitSeconds` is cumulative acquire duration, not exclusively queue wait. Logical shared-buffer accesses are not physical IOPS. Database CPU is cgroup CPU time including observer/background work and a small monitoring margin; use CPU time per task rather than interpreting these short windows as utilization. Finalize row counts include the outcome event and therefore are not task counts.

## Results, 2026-09-15

Both revisions passed **24/24 cases**. Every case completed all 2,000 tasks, with no repeated task attempts, renewal errors/losses or remaining permits. Neither recorded a deadlock. The slot implementation recorded no transaction SQLSTATE errors; the counter implementation recovered 36,276 `55P03` finalize errors in its six limited-tag cases. Intentional shutdown cancellation is excluded from transaction-error acceptance.

Times below are medians of three repetitions; parentheses show the slot implementation's observed minimum–maximum. All individual values, including the counter ranges and transaction percentiles, remain in the JSON.

| Workload | Execution slots | Counter drain, s | Slot drain, s (range) | Slot completed tasks/s |
| --- | ---: | ---: | ---: | ---: |
| Untagged | 100 | 1.251 | 0.969 (0.896–1.206) | 2,064 |
| Untagged | 200 | 0.953 | 0.806 (0.760–0.852) | 2,481 |
| Shared unlimited tags | 100 | 1.088 | 0.933 (0.846–1.131) | 2,143 |
| Shared unlimited tags | 200 | 1.148 | 1.131 (1.094–1.171) | 1,768 |
| Shared limited tags | 100 | 16.154 | 2.096 (2.084–2.372) | 954 |
| Shared limited tags | 200 | 16.757 | 2.067 (2.056–2.315) | 968 |
| 256 serial queues + 100,000 historical rows | 100 | 1.305 | 1.121 (1.038–1.230) | 1,785 |
| 256 serial queues + 100,000 historical rows | 200 | 1.100 | 1.037 (0.984–1.371) | 1,929 |

For the limited-tag workload, median completed throughput improved **7.71× at 100 execution slots** and **8.11× at 200**. Across its 12,000 tasks, the slot implementation made exactly 12,000 finalize calls with **zero `55P03` retries**, compared with 48,276 finalize calls on the counter implementation. This is consistent with removing both the shared release guard and the 250 ms persistent-waiter wakeup dependency; it is not a separate attribution experiment for those two changes.

| Limited-tag metric, median per 2,000-task queue | Counter, 100 | Slots, 100 | Counter, 200 | Slots, 200 |
| --- | ---: | ---: | ---: | ---: |
| Database CPU, ms/completed task | 5.582 | 1.004 | 7.235 | 1.060 |
| Business claim SQL calls | 1,451 | 182 | 1,120 | 180 |
| Empty claim transactions | 1,359 | 117 | 1,026 | 114 |

Other workloads show much smaller differences, with several overlapping run ranges. Unlimited tags make zero shared counter updates on both revisions. The 100-slot limited-tag finalize transaction p99 is higher on slots (median 42.7 ms versus 8.9 ms), while the queue drains much faster: the counter's distribution contains thousands of fast failed transactions and omits inter-retry backoff. These transaction percentiles alone do not measure handler-to-committed-outcome latency.

Both builds also logged shutdown rollback diagnostics (`failed to deallocate cached statement(s): conn closed`): 11 on counter and 10 on slots. Their case attribution, counts and timestamps are retained separately as `logErrors`. Passing workload acceptance does not mean an empty error log.

## Correctness and chaos

The slot implementation passed a fresh **200-iteration PostgreSQL 17 chaos run**, seed 424242, in **611.37 seconds**. It submitted 864 tasks: 860 completed, four cancelled, none pending/running/failed at the end. Fault injection included 21 Worker disruptions, 13 PostgreSQL restarts, eight control-plane outages and 103 runtime configuration updates. A durable audit recorded 587 slot allocations, zero quota/ownership violations, global/group peaks of 3/2 and fully drained permits. Gates also verified full-limit release, takeover after owner death and takeover after isolating an owner's database connections. See the [chaos summary and assertions](benchmarks/task-slot-admission-chaos-200-2026-09-15.json); its audit events are slot allocations, unlike the historical counter-increase audit.

The [validation record](benchmarks/task-slot-admission-validation-2026-09-15.json) includes:

- `go test -race -tags=ut ./... -timeout=6m` and the deterministic taskcore suite.
- PostgreSQL 15 and 17 admission, tag, lifecycle, renewal and migration smoke suites under `-race`, including 14→15→14 rollback.
- Three PostgreSQL 17 stress repetitions, sustained tag-load tests and strengthened slot regressions under `-race`.

Regression cases cover independent finalization while allocation guards remain held, an uncommitted slot allocation/release leaving other slots usable, atomic multi-tag rollback, restoring an expired attempt's old allocation after a rejected reclaim, quota activation/removal/backfill/shrink, retired-owner progress, concurrent batches, control bypass, labels/strict/serial routing, 100,000 historical rows and 100 execution slots filled by four batch queries. An explicitly injected 900 ms finalize rollback window outlasts the original 600 ms lease while renewal continues and the handler executes once. Ordinary slot release no longer supplies that fault.

## Remaining cost

A separate direct `LIMIT 1` sustained-load test used 16 claim/finalize/replenish loops under `-race`, five-second windows and default Docker resources. It passed the regression budgets, but with 20,000 zero-capacity blocked tasks the mixed workload achieved only **81.9 completed tasks/s**, with successful-claim **p95 241.8 ms / p99 382.0 ms**. Its full measurements are retained in the validation JSON. It uses different limits, handlers, resource settings and a different admission path from the automatic benchmark, so these rates are not a before/after comparison.

Slots remove shared accounting writes and persisted wakeup state. They do not remove the cost of filtering large blocked candidate sets or guarantee fairness across arbitrary tag combinations. Slot storage/configuration work is proportional to configured capacity, and quota backfill takes an exclusive task-table barrier. These remain relevant constraints. Local benchmark gains do not establish a production concurrency ceiling or justify moving directly to 400/1,000/2,000 workers.

## Reproduce

Build the same opt-in test at each recorded revision, use separate report directories, and alternate invocations without concurrent load tests:

```sh
GOMAXPROCS=2 ANCLAX_ADMISSION_BENCH=1 \
  ANCLAX_ADMISSION_BENCH_REVISION="$(git rev-parse HEAD)" \
  ANCLAX_ADMISSION_BENCH_TIMEOUT=90 ANCLAX_TEST_REPORT_DIR=/tmp/admission-slots \
  ANCLAX_SMOKE_POSTGRES_IMAGE=postgres:17 \
  go test -tags=smoke ./pkg/taskcore/e2e -run '^TestTaskAdmissionBenchmark$' -count=1 -v -timeout=20m

ANCLAX_TASKCORE_CHAOS_POSTGRES_IMAGE=postgres:17 \
  ANCLAX_TASKCORE_CHAOS_ITERATIONS=200 go test -tags=smoke ./pkg/taskcore/chaos \
  -run '^TestContainerizedTaskcoreChaosSmoke$' -count=1 -v -timeout=60m
```

For the counter revision, copy only the unchanged benchmark file from `4453050` into a detached checkout at `caf7332` before building. The final revision already contains it. Each invocation creates and removes its own database container. See [tag-concurrency verification](async-task-tag-concurrency.md#verification) for smoke commands and [upgrade instructions](async-task-tag-concurrency.md#storage-compatibility-and-upgrade) before applying migration 15. No production deployment or PR merge was performed for these measurements.
