# Sustained scheduling capacity

The primary performance question is how much continuous task turnover Anclax can sustain for a fixed database and connection budget. Startup/recovery delay has a separate acceptable budget; finite-job drain time is an auxiliary measurement. A large configured concurrency or a set of parked tasks does not establish scheduling throughput.

`TestTaskSchedulingCapacityBenchmark` uses the automatic Worker, pending-to-ready admission, ready claims, normal finalization, and execution-lease renewal. The production controller is unchanged by this benchmark addition. Main performance criteria are sustained healthy throughput and resource use; an acceptable recovery-latency budget is assessed separately from capacity. The production empty-result retry cap remains 100 ms in this iteration.

**Observed resource-efficiency point:** 5,000 configured slots with mean 10-second tasks sustained about 495 completions/s with both 100 and 30 total connections. Both runs passed health checks; the 30-connection sample used 13.2% less DB CPU per completion. This is one matched comparison. Larger targets did not qualify, so these results establish tested operating points, not a framework concurrency ceiling.

## Workload and resource model

- `near_empty`: independently scheduled arrivals below the handler's nominal execution capacity. Check arrival/completion balance, backlog trend, resource cost and producer delivery shortfall.
- `backlogged`: seed twice the configured concurrency as pending, then continue independently scheduled arrivals above nominal execution capacity. Warmup is bounded even if the configured target is unreachable. Check actual execution concurrency, stable completion rate, ready-stock changes and runnable supply.
- Handler durations control turnover; tasks wait on timers without holding a connection or issuing business SQL. For example, 10,000 simultaneous 10-second handlers require roughly 1,000 starts and completions per second once stable; 10,000 60-second handlers require only about 167. Thus concurrency-per-connection is only meaningful with task duration, renewal settings and throughput alongside it.
- `HandlerJitter=0` keeps fixed durations and exposes synchronized completion waves. `HandlerJitter=0.5` deterministically disperses task duration between 50% and 150% of the nominal duration, with approximately the same mean. The primary large-capacity scan uses dispersion; the fixed-duration stress results remain separate. This is a reproducible synthetic distribution, not an assumed production distribution.
- `Connections` is the **total configured budget**: main pool + renewal pool + one producer + one observer. A budget of 100 with 10 renewal connections leaves 88 main connections; 200 leaves 188. Database `max_connections` includes additional administrative headroom which the fixture does not use as worker capacity. Pool creation and acquired occupancy are reported separately from the budget.
- `RenewalIntervalMS`, `HeartbeatMS` and `LockTTLMS` make lease/registry load explicit. Fixture defaults retain the earlier stress settings of 1,000/1,000/9,000 ms; the comparison also uses 3,000/3,000/9,000 ms. The framework defaults to a 3-second registry heartbeat and uses that interval for renewal unless overridden. Both intervals change in the comparison, so it is not a single-variable renewal-only ablation.
- The fixture runs one Worker runtime/process. PostgreSQL has two CPU and 2 GiB; Go's CPU parallelism is recorded. Resource increases other than connection budget must be separate experiments.

The producer follows elapsed wall time, coalesces arrivals when necessary and commits at most 256 inserts per transaction. It catches up to the planned schedule instead of adapting offered load to observed completions. Submission shortfall, actual arrival rate and maximum delivery lag expose producer/database ingestion limits. Seed work is admitted normally, never directly written as ready. Initial seed creation occurs before the run; continuing enqueue cost is included in steady measurement.

## Measurement and acceptance

Each case has an explicit warmup and fixed measurement duration. Five-second windows report committed completions/s, actual arrivals/s, mean executing handlers, database/process CPU and connection wait. No final drain contributes to throughput. The runtime is canceled after measurement; unfinished work is intentional and reported at the boundary.

An in-memory observer increments completion only after successful finalization commit, then cross-checks against completed database rows after shutdown. Handler concurrency is integrated across every entry/exit, not inferred from configured slots or a sparse gauge. The fixture adds no per-task audit table or trigger. It does inspect nonterminal rows and database statistics every five seconds, and samples pool occupancy about every 100 ms. Database CPU includes ongoing inserts, scheduling, finalization, renewal and observer work. A state-count query can still visit historical tuples depending on the PostgreSQL plan; filtering its result is not proof of physical scan avoidance. Process CPU includes Worker, producer and benchmark accounting, but excludes Docker child-command CPU.

Snapshots are not atomic across the database, producer and process. Collection duration is retained. Small boundary discrepancies and short-lived pool peaks can be missed; pool means are sample means, and sampled maxima are lower bounds. Go heap/system allocation is not RSS. A `SupplyGapSamples` observation means due unconstrained pending exists, ready is empty and database running count is below configured concurrency; it is not a precise decomposition of claim/execute/finalize slot time. Ready depletion across windows must be considered before interpreting throughput as sustainable. If no task completes, the raw zero-valued CPU-per-completion field is undefined, not zero cost; failed/stopped cases must not be ranked as resource savings.

`Correct` checks committed-count reconciliation, repeated attempts, measured scheduler errors, deadlocks and lease errors/loss. Failures preserve evidence. Shutdown cancellation errors are outside the measured interval; warmup scheduler/runtime errors are reported separately. The final fixture also records whether the Worker returned before measurement ended and includes runtime errors in health checks. `TargetReached` is separate: the unconstrained backlogged reference requires at least 90% mean handler occupancy **and** 90% of nominal completion throughput; near-empty requires at least 98% of offered rate completed without material backlog growth. It also requires correctness and no warmup scheduler/runtime errors. `ProducerKeptUp` requires at least 98% delivered rate and a bounded endpoint shortfall. These flags are screening aids, not capacity guarantees; inspect interval trends and raw shortfall/stock values.

## Coverage

The initial scan varies 100 ms, 1 s, 10 s and 60 s handlers, total budgets of 30/100/200 connections, and configured execution concurrency up to 40,000. It first measures unconstrained work to isolate scheduling capacity. It is an exploratory sequential scan, not a repeated before/after comparison or a production capacity certification. Connection comparisons at the same concurrency retain arrival rate; higher-concurrency targets also require higher offered load. Changes between those targets cannot be attributed to configured concurrency alone. See the recorded results below for which targets actually ran and succeeded.

The existing [arrival benchmark](arrival-prefetch-benchmark.md) retains burst, mixed-duration, hot-quota, serial and stopped-consumption recovery cases; [protocol validation](durable-ready-prefetch-benchmark.md) covers separate concurrency/recovery properties. They supplement this test rather than substitute for sustained turnover.

Further capacity dimensions include mixed duration distributions, hot quota combinations, serial history, heterogeneous routing/strict/weight groups, multiple processes and renewal pools, background business SQL, database CPU/IO scaling, long-term MVCC/autovacuum behavior and prolonged soak. A fixed timer distribution deliberately isolates capacity; it cannot cover every production workload.

## Reproduce

Build the smoke test binary once, then run it without concurrent builds/tests:

```sh
go test -c -tags smoke ./pkg/taskcore/e2e -o /tmp/anclax-capacity.test
ANCLAX_SCHEDULING_CAPACITY=1 \
ANCLAX_SCHEDULING_CAPACITY_CASES='[{"Name":"p100-c10000-10s","Mode":"backlogged","Connections":100,"RenewalConnections":10,"Concurrency":10000,"HandlerMS":10000,"HandlerJitter":0.5,"RenewalIntervalMS":3000,"HeartbeatMS":3000,"LockTTLMS":9000,"ArrivalRate":1200,"WarmupSeconds":60,"MeasureSeconds":120}]' \
ANCLAX_TEST_REPORT_DIR=/tmp/anclax-capacity-results \
GOMAXPROCS=2 /tmp/anclax-capacity.test \
  -test.run='^TestTaskSchedulingCapacityBenchmark$' -test.v -test.timeout=12m
```

The default cases are smaller (100 slots, 1-second handlers, two 120-second measurements) and remain opt-in. All tasks belong to an isolated Docker PostgreSQL container, removed during cleanup. The fixture does not access production or application repositories. No new chaos run is required for this measurement-only change.

## Recorded results

Measured 2026-09-16 on Apple M5 / Go 1.26.5 arm64, `GOMAXPROCS=2`, PostgreSQL 17.11 with two CPU and 2 GiB. The user’s applications and existing nile-k0s container remained running. Each configuration ran once, sequentially, in the archived order; no agent builds, race tests or chaos overlapped timed runs. These are exploratory observations, not confidence intervals or a certified capacity limit.

Production behavior remains `f0bb64e` (the initial scan’s repository head `a9a8089` adds documentation). The fixture was developed in three explicit versions: fixed-duration measurements; deterministic duration dispersion and a throughput target; then configurable lease intervals, runtime errors and Worker-exit detection. Final harness commit: `416d261`. Exact sources, binary/source hashes, order, parameters and every failure are retained. The four small fixed-duration cases remain auxiliary results; no before/after performance claim combines different workload distributions.

### Low-load and smaller saturated cases

All use a 100-connection budget and one-second renewal/registry heartbeat. Fixed durations are intentional here. The offered rate in backlogged cases exceeds service capacity, so growing pending backlog is expected.

| Mode / duration / slots | Offered/s | Completed/s | Mean executing | DB CPU cores | DB CPU ms/completion | Outcome |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| near_empty / 100 ms / 100 | 700 | 700.3 | 70.1 | 0.64 | 0.915 | Healthy; target reached |
| backlogged / 100 ms / 100 | 1,200 | 882.6 | 88.4 | 0.73 | 0.832 | Healthy; below 90% reference |
| near_empty / 1000 ms / 1,000 | 700 | 699.6 | 700.2 | 0.58 | 0.827 | Healthy; target reached |
| backlogged / 1000 ms / 1,000 | 1,200 | 970.1 | 969.7 | 0.84 | 0.862 | Healthy; target reached |

### Continuous large-target scan

These handlers have durations uniformly dispersed from 50% to 150% of the nominal 10 seconds. Each case uses a 60-second warmup and 120-second observation. All backlogged cases in this report recorded **zero empty prefetch results during measurement**: the empty-result retry backoff was not activated in those intervals. This says nothing about finite-quota or intermittently empty workloads. Some large targets still sampled an empty ready set; positive admission calls do not prove that prefetch always stays ahead.

| Total connections | Target slots | Renewal / heartbeat | Completed/s | Mean executing | DB CPU cores | Full-run outcome |
| ---: | ---: | --- | ---: | ---: | ---: | --- |
| 30 | 5,000 | 1s / 1s | 497.0 | 4,952 | 0.77 | Healthy; target reached |
| 100 | 5,000 | 1s / 1s | 493.5 | 4,950 | 0.88 | Healthy; target reached |
| 30 | 10,000 | 1s / 1s | 0.0 | 0 | — (no progress) | Not qualified |
| 100 | 10,000 | 1s / 1s | 922.0 | 9,272 | 1.81 | Not qualified |
| 200 | 10,000 | 1s / 1s | 798.8 | 8,252 | 1.94 | Not qualified |
| 200 | 20,000 | 1s / 1s | 747.1 | 8,232 | 2.00 | Not qualified |
| 200 | 30,000 | 1s / 1s | 544.8 | 5,873 | 1.96 | Not qualified |
| 200 | 40,000 | 1s / 1s | 707.0 | 7,273 | 2.00 | Not qualified |
| 100 | 10,000 | 3s / 3s | 898.6 | 9,127 | 1.87 | Not qualified |
| 200 | 20,000 | 3s / 3s | 911.3 | 9,833 | 1.98 | Not qualified |

Failed runs are not sustainable-capacity or resource-efficiency results. The 30-connection / 10,000-slot case stopped making progress before measurement: it retained ready work and unfinished database running rows. Its low CPU and zero-valued CPU-per-completion field must not be interpreted as savings. The older fixture did not directly record Worker return, so that run does not identify why progress stopped.

At higher slot targets the producer also fell behind. Its maximum delivery lag was 7.1s at 200/10,000, 9.8s at 200/20,000, 45.4s at 200/30,000 and 91.8s at 200/40,000 under one-second renewal. The three-second 200/20,000 case reached 11.9s. These failures cover the combined insertion/scheduling pipeline; they do not isolate a Worker-only maximum. Pending inventory remained available, but planned and delivered arrival rates must not be conflated.

### Failure accounting

Errors/loss below are increments within the measurement window. Repeated tasks are rows with `attempts > 1` after shutdown, including warmup; that is a count of affected tasks, not extra attempts or a steady-only duplicate rate. Warmup errors remain in the raw snapshots and final fixture’s explicit warmup fields.

| Case | Measured finalize errors | Measured renewal errors / lost | Repeated tasks over whole run |
| --- | ---: | ---: | ---: |
| 30 connections / 10,000 slots / 1s renewal | 0 | 0 / 0 | 0 |
| 100 connections / 10,000 slots / 1s renewal | 421 | 3 / 750 | 2,792 |
| 200 connections / 10,000 slots / 1s renewal | 366 | 0 / 0 | 4,050 |
| 200 connections / 20,000 slots / 1s renewal | 3,985 | 17 / 5687 | 8,278 |
| 200 connections / 30,000 slots / 1s renewal | 1,594 | 12 / 1954 | 5,433 |
| 200 connections / 40,000 slots / 1s renewal | 0 | 0 / 0 | 4,822 |
| 100 connections / 10,000 slots / 3s renewal | 89 | 0 / 0 | 2,672 |
| 200 connections / 20,000 slots / 3s renewal | 2,002 | 3 / 1583 | 26,541 |

All measured deadlock increments were zero in this unconstrained fixture. Error logs include finalization context deadlines; some cases also failed prefetch observations. The three-second 100/10,000 case had no new renewal error/loss in measurement, but still recorded 89 finalize errors and 2,672 repeated tasks over the whole run, so the three-second interval configuration still did not qualify it.

### Fixed-duration wave and long-task comparison

The initial fixed 10-second, 100-connection / 10,000-slot stress case averaged 752.5 completions/s and 7,372 executing handlers. It had 7,659 repeated tasks after startup failures. Its measured ready samples stayed between 1,197 and 2,828 and empty prefetch results stayed zero. A diagnostic SQL sample was taken after failures were already present, briefly opening one extra observer connection; this failed run is diagnostic evidence, not a clean budget-qualified throughput result.

That diagnostic’s cumulative top-level SQL time was dominated by `FinalizeTaskAttempt` and `RefreshTaskLocks`, and the live snapshot included `BufferContent` and `WalSync` waits. SQL time includes waits and parallel operations; it is not a CPU percentage or complete causal attribution. Together with pool wait and deadline errors, it motivates profiling finalization and renewal under load. It does not establish an untested SQL fix.

The dispersed 60-second, 100-connection / 10,000-slot case used a 120-second warmup and 180-second observation. It averaged 167.9 completions/s and 9,992 executing handlers at 0.91 DB CPU cores, with no new measured scheduler/renewal errors or lease loss. Warmup nevertheless included four renewal errors, 1,017 lost leases and nine finalize errors, and 989 tasks had repeated attempts by the end. It demonstrates a much lighter steady turnover requirement, but is not a fully healthy capacity qualification.

### Matched connection-budget reduction

At 5,000 slots, mean 10-second handlers, 600 offered tasks/s and one-second renewal/heartbeat, reducing the total budget from 100 to 30 changes observed completion rate from **493.5 to 497.0/s (+0.7%)**. Full-run correctness/target results: **True/True → True/True**. This is a single matched workload comparison, not a statistical equivalence test.

| Budget | Mean executing | Main / renewal mean acquired | Main / renewal sampled connection maxima | DB CPU ms/completion | Main aggregate acquire wait seconds |
| ---: | ---: | --- | --- | ---: | ---: |
| 100 | 4,950 | 4.56 / 0.48 | 88 / 10 | 1.793 | 61.4 |
| 30 | 4,952 | 2.38 / 0.32 | 18 / 10 | 1.556 | 1,570.5 |

Acquire waits are accumulated across requests, not wall-clock stall duration. Mean acquired connections do not establish a safe pool limit: transient demand and control/renewal progress also matter. Lower connection budget is not automatically lower CPU per completion. The data should be used to select and repeat healthy operating points, rather than assume that doubling connections doubles task capacity.

### Validation and evidence

- Focused race validation passes for both low-load and backlogged fixture lifecycles, including the final configurable three-second renewal case. A deterministic distribution check validates duration bounds and preservation of nominal mean. The final fixture additionally detects early Worker return and records runtime errors.
- Sixteen exploratory configurations ran. Capacity failures are retained and reported; they are not test passes. No production scheduler change, deployment, new chaos run or claims of a newly fixed concurrency protocol are included.
- No raw JSON or benchmark binaries are committed. The archive contains exact fixture versions, runner scripts, metadata/hashes, all raw windows and failure logs, validation and the diagnostic SQL sample.

Verified local archive: `/tmp/anclax-steady-capacity-evidence-2026-09-16.tar.gz` (438,421 bytes, 65 files), SHA-256 `2fdf3c4d9a8c92c51e2fd1a2af4be44bd3d9900fb6e2f11b8e884f728ec28fc2`. Internal contents and hashes were checked.
