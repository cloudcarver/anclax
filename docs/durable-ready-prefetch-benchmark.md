# Durable ready and consumption-based prefetch

> Historical benchmark report. These benchmark harnesses have been retired; current capacity testing uses the [sustained scheduling benchmark](scheduling-capacity-benchmark.md). Reproduction commands below apply to the recorded revisions and archived harnesses, available in the [source snapshot](https://github.com/cloudcarver/anclax/tree/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/pkg/taskcore/e2e). Raw result links point to Git history; generated JSON/log reports are no longer stored in the current tree.

Measured on 2026-09-16. Production source: `13249773e7c7f22d6f9d77f4bdd9a89c1af3cd9d`; baseline: `d5e463f78cd53b48c4b7011c27ca655d6aa2f9c3` (the previous supply-first policy). Later changes only publish documentation.

Ready now persists until consumption or explicit invalidation, with no TTL or fixed stock ceiling. Per-group stock departures estimate consumption and decide when candidate computation can pause. This removes idle candidate calls and builds a larger reserve for unconstrained work, but **does not establish a general throughput or CPU improvement**: 6/14 short-case medians improve and 8/14 decline. The PR remains a draft.

## What changed

- Remove the two-second ready expiry, ready recovery, the `min(4096, live Worker capacity)` limit and the separate strict-ready count limit. Worker execution leases and strict execution allowances remain.
- Ready holds the finite tag slots and serial ownership already computed at admission. Time passing, scheduler takeover and disconnected Workers do not revoke it. Cancel, pause and scheduling edits still invalidate unconsumed reservations atomically. No second queue table is introduced.
- Observe stock separately by limited-tag set, routing labels and normal/strict priority class. The priority-class split prevents stock for disabled strict execution from suppressing normal admission. Unlimited tags still allocate no shared counters or slots; quota activation backfills admission snapshots.
- Target `max(256, 0.5 seconds × observed consumption rate)` per group. This is a soft refill target, not a queue limit. Rates rise immediately and decay with a one-second time constant; empty due groups bootstrap even at zero measured consumption. Each admission transaction remains bounded to 256 tasks.
- Stop candidate SQL for stocked groups while allowing other groups to progress. Independent fenced observations run on a 50 ms busy / up-to-100 ms idle schedule; a 250 ms maintenance deadline can advance them. Running-lease recovery and deferred group classification continue while admission is stopped. Positive-resource blocked rounds retry at 5 ms. No claim-side consumption counter or notification mechanism is added.

Full semantics, migration and integration: [design](ready-task-prefetch.md).

## Method and accounting

Both revisions use identical final `admission_benchmark_test.go` and `prefetch_idle_benchmark_test.go` harnesses. The baseline worktree changes only those harnesses. Binary, harness, report and log hashes are retained in the evidence archive. Order: before-1, after-1, after-2, before-2, before-3, after-3, after-steady, before-steady. No timed measurements overlap our builds, stress suites or chaos runs.

PostgreSQL 17.11, Docker limit 2 CPU / 1 GiB, Apple M5, Go 1.26.5 arm64, `GOMAXPROCS=2`, main pool 50, renewal pool 10, claim batch 32. Each short case runs 2,000 tasks with a 20 ms handler at 100 or 200 Worker slots. Finite quotas use one global limit of 32 plus eight group limits of eight. Serial history has 100,000 terminal rows and 256 serial keys. Blocked cases configure a higher-weight zero-quota group with 0/20,000/200,000 blocked tasks plus 2,000 runnable tasks.

Tables show medians of three runs. Ranges are observed minima/maxima, not confidence intervals. CPU is total database execution CPU-seconds per completed case, including system admission, observations, claims, finalization and the observer. SQL timing sums top-level `pg_stat_statements` without nested double counting. Enqueue CPU/time includes indexes and group maintenance, but excludes migration/history setup and VACUUM. User applications and the existing `nile-k0s` container remained active; this is not an isolated performance lab.

## Throughput and database CPU

| Scenario | Slots | Tasks/s before → after | Change | Observed tasks/s range before → after | DB CPU seconds before → after | CPU change |
| --- | ---: | ---: | ---: | --- | ---: | ---: |
| untagged | 100 | 1666.3 → 1759.8 | +5.6% | 1595–1670 → 1290–1895 | 2.381 → 2.232 | -6.3% |
| untagged | 200 | 1771.6 → 1982.0 | +11.9% | 1632–2020 → 1565–2146 | 2.246 → 2.008 | -10.6% |
| unlimited_shared | 100 | 1451.1 → 1846.7 | +27.3% | 1376–1995 → 1776–1932 | 2.738 → 2.170 | -20.7% |
| unlimited_shared | 200 | 1660.6 → 1828.5 | +10.1% | 1568–1964 → 1403–2036 | 2.379 → 2.188 | -8.0% |
| limited_shared | 100 | 699.0 → 741.5 | +6.1% | 655–773 → 701–746 | 2.952 → 2.514 | -14.8% |
| limited_shared | 200 | 756.7 → 669.8 | -11.5% | 741–758 → 600–749 | 2.927 → 3.615 | +23.5% |
| serial_history | 100 | 1290.7 → 1207.2 | -6.5% | 1228–1375 → 1146–1346 | 3.090 → 3.261 | +5.5% |
| serial_history | 200 | 1378.7 → 1119.6 | -18.8% | 1355–1379 → 592–1385 | 2.883 → 3.529 | +22.4% |
| blocked_0 | 100 | 1598.5 → 1328.3 | -16.9% | 1440–1967 → 1292–1399 | 2.462 → 2.944 | +19.6% |
| blocked_0 | 200 | 1824.2 → 1503.4 | -17.6% | 1610–1834 → 1485–1688 | 2.213 → 2.669 | +20.6% |
| blocked_20000 | 100 | 1659.8 → 1461.4 | -12.0% | 1468–2000 → 1457–1607 | 2.429 → 2.747 | +13.1% |
| blocked_20000 | 200 | 1606.2 → 1355.7 | -15.6% | 1399–1876 → 1039–1534 | 2.557 → 2.960 | +15.8% |
| blocked_200000 | 100 | 1237.6 → 1126.5 | -9.0% | 1110–1337 → 938–1354 | 3.227 → 3.563 | +10.4% |
| blocked_200000 | 200 | 1077.6 → 1387.1 | +28.7% | 1002–1316 → 1164–1466 | 3.736 → 2.989 | -20.0% |

Unconstrained medians improve, while serial and most blocked combinations regress. `blocked_0` at 100 slots has non-overlapping observed throughput ranges; most other comparisons overlap. The 200-slot serial case includes a new-version run at 592 tasks/s, so its variation must not be hidden by the median. These measurements do not isolate the effects of durable state, cap removal, pacing, observation queries and indexing.

## Candidate calls, observations and stock

The harness names the admission operation `PrefetchReadyTasks`; integrated calls use `PrefetchTaskSupply`. Prepared counts come from the function result, not SQL result-row counts. `InspectTaskPrefetch` is a separate operation whose cost is included above.

| Scenario | Slots | Admission calls before → after | New observation calls | Zero-prepared calls before → after | Sampled peak ready before → after | Supply-gap samples % before → after |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| untagged | 100 | 83 → 9 | 15 | 20 → 1 | 100 → 1272 | 0.0 → 0.0 |
| untagged | 200 | 54 → 8 | 12 | 8 → 0 | 200 → 1241 | 0.0 → 0.0 |
| unlimited_shared | 100 | 85 → 8 | 15 | 19 → 0 | 100 → 1417 | 2.7 → 0.0 |
| unlimited_shared | 200 | 50 → 8 | 13 | 6 → 0 | 200 → 1288 | 0.0 → 0.0 |
| limited_shared | 100 | 461 → 445 | 54 | 373 → 352 | 32 → 32 | 74.6 → 64.9 |
| limited_shared | 200 | 433 → 436 | 57 | 302 → 334 | 32 → 32 | 45.8 → 63.5 |
| serial_history | 100 | 56 → 44 | 19 | 15 → 12 | 100 → 256 | 17.0 → 0.0 |
| serial_history | 200 | 28 → 30 | 14 | 6 → 10 | 200 → 256 | 15.6 → 31.0 |
| blocked_0 | 100 | 85 → 8 | 18 | 19 → 0 | 100 → 1274 | 2.6 → 0.0 |
| blocked_0 | 200 | 53 → 8 | 15 | 8 → 0 | 200 → 1192 | 0.0 → 0.0 |
| blocked_20000 | 100 | 88 → 8 | 18 | 25 → 0 | 100 → 1235 | 2.7 → 0.0 |
| blocked_20000 | 200 | 54 → 8 | 15 | 12 → 0 | 200 → 1087 | 0.0 → 0.0 |
| blocked_200000 | 100 | 84 → 8 | 21 | 22 → 0 | 100 → 1237 | 0.0 → 0.0 |
| blocked_200000 | 200 | 48 → 8 | 14 | 10 → 0 | 200 → 1149 | 0.0 → 0.0 |

Twenty-millisecond samples exclude startup and tail. A supply-gap sample means pending > 0, ready = 0 and running < configured Worker slots. It does **not** establish runnable starvation when finite tags or serial ownership prohibit further admission. Peaks are sampled, not exact maxima. Table percentages are medians of per-run percentages.

Ready exceeds Worker capacity in unconstrained cases, while real resource limits still bound finite/serial reservations. Fewer admission calls do not automatically mean less CPU: batches are larger, observations add work and pre-admission changes when resource reservations occur. Even at 100 serial slots, eliminating sampled ready gaps did not improve total throughput. The aggregate measurements do not prove a single cause for the regressions.

## Enqueue cost

| Scenario | Slots | Enqueue elapsed ms before → after | Enqueue CPU seconds before → after | Enqueue + execution CPU change |
| --- | ---: | ---: | ---: | ---: |
| untagged | 100 | 175.5 → 185.8 | 0.0490 → 0.0602 | -5.7% |
| untagged | 200 | 155.7 → 160.3 | 0.0477 → 0.0531 | -10.5% |
| unlimited_shared | 100 | 198.6 → 169.7 | 0.0804 → 0.0697 | -20.7% |
| unlimited_shared | 200 | 182.4 → 170.2 | 0.0791 → 0.0701 | -8.3% |
| limited_shared | 100 | 190.8 → 182.9 | 0.0807 → 0.0786 | -13.4% |
| limited_shared | 200 | 206.5 → 201.6 | 0.0806 → 0.0789 | +22.8% |
| serial_history | 100 | 183.8 → 187.5 | 0.0613 → 0.0582 | +5.4% |
| serial_history | 200 | 187.5 → 182.1 | 0.0583 → 0.0585 | +22.2% |
| blocked_0 | 100 | 203.2 → 195.3 | 0.0753 → 0.0712 | +20.2% |
| blocked_0 | 200 | 159.5 → 208.1 | 0.0654 → 0.0791 | +20.4% |
| blocked_20000 | 100 | 838.2 → 822.7 | 0.6815 → 0.6836 | +11.2% |
| blocked_20000 | 200 | 924.0 → 952.7 | 0.7458 → 0.7978 | +11.9% |
| blocked_200000 | 100 | 6890.2 → 7463.3 | 6.5378 → 7.1467 | +9.7% |
| blocked_200000 | 200 | 7244.1 → 7484.6 | 6.9455 → 7.0277 | -5.0% |

Combined CPU is the median of each run's enqueue-plus-execution total, not the sum of separate medians. Blocked insertion is included; the large insertion cost must not be omitted when evaluating end-to-end database work.

## Idle and first arrival

Each run warms for two seconds, observes five idle seconds, then inserts 100 tasks with a 20 ms handler. These are fixed-phase first-completion observations, not latency percentiles.

| Metric | Before | After |
| --- | ---: | ---: |
| DB CPU seconds | 0.1324 | 0.1378 |
| Top-level SQL ms | 29.11 | 27.74 |
| Candidate/admission calls | 52 | 0 |
| Observation calls | 0 | 62 |
| Business claim calls | 259 | 257 |
| First completion ms | 186.6 | 154.8 |
| Drain ms | 395.3 | 410.5 |
| First completion range ms | 181.8–218.4 | 102.3–281.9 |

Idle candidate computation stops, but observation, maintenance, Worker polling and lease renewal continue. DB CPU changes from 0.1324 to 0.1378 seconds (+4.1%); this does not show an idle CPU saving. The earlier median first completion has a wider range and does not establish a tail-latency guarantee. All six arrivals finish all 100 tasks with 100 attempts.

## Matched longer finite-quota run

One additional pair runs 20,000 finite-quota tasks at 100 Worker slots; it is excluded from the short-case medians. This is about 27 seconds per version, not a production endurance run.

| Metric | Before | After |
| --- | ---: | ---: |
| Tasks/s | 729.7 | 746.2 |
| Elapsed seconds | 27.41 | 26.80 |
| Execution DB CPU seconds | 31.072 | 27.566 |
| Enqueue DB CPU seconds | 0.810 | 0.794 |
| Top-level SQL ms | 31598.8 | 28587.3 |
| Peak ready | 32 | 32 |
| Admission calls | 4594 | 4518 |
| Zero-prepared calls | 3476 | 3421 |
| Observation calls | 0 | 526 |

Before roughly one-second completion samples, excluding first and tail buckets: median **753.9/s**, range **536.2–831.2/s**, 26 samples.

After roughly one-second completion samples, excluding first and tail buckets: median **777.8/s**, range **631.8–827.3/s**, 25 samples.

This pair improves whole-run throughput by 2.3% and reduces execution DB CPU by 11.3%. It does not establish stable throughput across repeated long runs or resolve the short 200-slot finite-quota regression. Both versions still make thousands of zero-prepared calls under finite-resource contention; removing a virtual-queue cap does not remove that retry cost.

## Correctness and evidence

All **84 short busy cases** pass: 168,000 completions and attempts, zero repeated tasks, remaining permits, lease errors/loss or recorded operation SQLSTATE errors. The two longer runs add 40,000 exactly-once completions with the same zero-error assertions. Six idle runs add 600 tasks: **208,600 benchmark completions/attempts in total**. Evidence retains 16 baseline and six new-version shutdown rollback log messages from cancellation of in-flight work; these are not hidden or reported as operation SQLSTATE failures. Neither long run logs a rollback error.

Validation on final production revision `1324977`:

- `go test -race ./...` passes.
- PostgreSQL 15 and 17 race smoke suites pass: ready ownership and invalidation, finite/unlimited tags, quota backfill, slots, serial/FIFO, weighted/strict scheduling, lifecycle, migration rollback, query plans and batch renewal.
- Regressions verify ready survives beyond the former TTL and scheduler loss; 5,000 ready tasks with Worker capacity one; 2,500 strict ready tasks with a one-task claim allowance; maintenance/classification while admission is stopped; new routes/groups; quota enablement without consumption; consumption restart; strict stock cannot mask normal work after strict execution is disabled.
- Three complete DST scenario repeats pass. The PR retains the executable regression for propagating generated-script assertion failures; older DST reports before that fix did not reliably establish every assertion.
- Fresh **200 chaos rounds** pass: 21 Worker disruptions, 13 PostgreSQL restarts, eight control outages, 103 configuration updates and 26 replacement Workers. 864 submitted, 860 completed, four cancelled, four retried; zero pending/ready/running/failed/paused business tasks at drain. Tag audit: **587 observations, zero violations, zero terminal memberships**, permits drained. Both helper binaries report clean `1324977` VCS metadata.

The initial uncommitted pilot exposed an observation `EXISTS` plan scanning 200,006 pending rows. The final ordered lateral `LIMIT 1` query retains group/due index access, and real-plan regressions pass on PostgreSQL 15/17. A second exploratory pilot predates the priority-class correctness fix. Both pilot sets and the before/after plans are preserved but **excluded from final medians**.

Raw evidence, scripts, hashes, harnesses, metadata and the production patch are local only; no new raw JSON is committed. The archive verifies all internal file hashes and extracted contents:

- Path: `/tmp/anclax-durable-ready-evidence-2026-09-16.tar.gz`
- Size: **274,974 bytes**, 58 files.
- SHA-256: `69d8685695fc798245990e1169bcb6f707966dfe343f1cd67fa4b9d8d5a1ffde`.

The archive's runner records exact commands and environment. Compile identical smoke-test binaries from the two revisions with the final harnesses, then run `TestTaskAdmissionBenchmark` and `TestTaskPrefetchIdleBenchmark` in the stated order. `ANCLAX_ADMISSION_BENCH=1` enables the fixture; scenario, concurrency, task count, history, handler duration and report-directory variables are recorded in its runner and metadata.

## Remaining limits

Ready is a virtual queue over existing task rows, but state/index updates and counting deep ready groups still use PostgreSQL resources. Many distinct resource/routing/priority-class groups need separate scale testing. Ready continues reserving finite slots and serial ownership while waiting for consumption; explicit task changes revoke reservations, time alone does not. These semantics are intentional.

The global scheduler, finite resources, serial constraints, finalization and database CPU remain independent limits. Stocked-group pauses reduce unnecessary candidate work but do not guarantee that prefetch can always outrun execution. The observed regressions and remaining blocked retries need further profiling before claiming a universal performance gain. Historical benchmark ratios must not be multiplied into these results. No production or brazosd access was used.
