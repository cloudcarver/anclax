# Consumption scheduling and empty-result backoff

> Historical benchmark report. These benchmark harnesses have been retired; current capacity testing uses the [sustained scheduling benchmark](scheduling-capacity-benchmark.md). Reproduction commands below apply to the recorded revisions and archived harnesses, available in the [source snapshot](https://github.com/cloudcarver/anclax/tree/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/pkg/taskcore/e2e). Raw result links point to Git history; generated JSON/log reports are no longer stored in the current tree.

Measured 2026-09-16. Baseline: `3f229c123c143a4e862cad2d8eb7a384109b003b`; new source: `f0bb64e36a9c95c62eba522d140253b29c9b7540`. Production policy changed in `c9a4a9a`, with a whole-task-deficit correction in `f0bb64e`; `668964c` corrects benchmark phase sampling. Later documentation commits do not change measured code.

The scheduler now separates normal consumption-based scheduling from empty-result retry deadlines. Ready remains durable and uncapped. This report adds persistent-runtime, independently scheduled arrival workloads to the existing short database benchmarks. It is synthetic Anclax load, not a replay of production traffic.

**The current parameters trade latency/throughput for fewer retries, and the PR remains a draft.** Mixed-workload DB CPU falls 13.9% with 2.0% longer drain; recovery CPU falls 17.9%, but first admission after a released slot rises from 8.7 to 43.8 ms. Finite-quota 20 ms tasks lose 24.5%–25.6% throughput for only 3.7%–6.2% less DB CPU. Those finite-quota throughput ranges do not overlap across the three runs. This setting does not yet meet a preference for keeping supply consistently sufficient under short-task quota turnover. Flow CPU rises 3.2% at the median, with substantial new-version variation.

The results support independent backoff as a way to reduce repeated unsuccessful work, but do not establish that the present delay/batch parameters are the best tradeoff. Changing forecast/batch sizing and retry timing together does not isolate the contribution of each change.

## Policy

- Target 500 ms of estimated consumption per group and refill below a 250 ms low watermark, with a minimum of one task. Remove the standing 256-task minimum reserve, and require a whole-task deficit before requesting another task. Groups without consumption pause when stocked; empty groups retain bootstrap/recovery requests of up to 256 tasks.
- Forecast stock depletion between observations to schedule and size normal refill batches. Observed departures account for our own admissions; forecast departures never feed back into measured demand. All estimates are advisory and PostgreSQL still authorizes admission.
- A zero result only imposes independent retry delays of 5, 10, 20, 40, 80 and at most 100 ms after transaction completion. Success clears retry backoff. Attempts must satisfy both normal demand timing and the retry deadline; zero output never changes the consumption estimate or permanently stops demand.
- Initial consumption discovery probes begin at 5 ms and back off toward 100 ms. Measured consumption uses 50 ms observations; idle observation is at most 100 ms, with maintenance able to advance it. Ready TTL/caps, claim-side counters and notifications remain absent. Existing SQL maintenance paths are unchanged; this iteration does not remove their duplicate checks.

See [design and integration](ready-task-prefetch.md).

## Workloads and measurement

Each version runs three times in order after-1, after-2, before-1, before-2, before-3, after-3. Two after runs are retained with verified hashes and timestamps proving they began after an earlier orphaned benchmark and validation ended. The overlapped baseline and orphan output are excluded; the remaining runs use an independent directory and cancellation now terminates child processes. This order is not a randomized or fully balanced experiment. The exact same final benchmark and support files are used in both binaries. PostgreSQL 17 / 2 CPU / 1 GiB, Apple M5, Go 1.26.5 arm64, GOMAXPROCS 2, main pool 50, renewal pool 10 and claim batch 32. None of the included timed runs overlap our builds, race tests or chaos. User applications and the existing nile-k0s container remain running.

| Dynamic scenario | Arrival schedule | Execution |
| --- | --- | --- |
| flow | Idle 2s; 50/s for 4s; 1,500/s for 6s; 100/s for 4s; idle 3s; 2,500/s for 3s; 200/s for 4s | 17,900 tasks, 20 ms handlers, one Worker with 100 slots |
| mixed | Idle 2s; 80/s for 6s; 200/s for 8s; 40/s for 4s; idle 3s; drain | 2,240 tasks, two Worker runtimes with 50 slots each sharing the main pool; 50% use one eight-slot hot tag; 10% use four serial keys; durations include 20/200/1,000/2,000 ms |
| recovery | Occupy all eight execution slots for 3s; queue quota waiters, unrelated ready stock and zero-quota work; enable quota at 4s; idle; new burst at 9s | 88 tasks, one Worker with eight slots; tests stopped consumption with ready stock, resource release without new consumption, quota enablement and arrivals after idle |

The producer follows preplanned timestamps independently of completions, using a nominal 20 ms coalescing delay and inserting at most 256 per transaction. If delivery falls behind, intended timestamps and producer lag remain in the report. Work is not silently dropped or rate-limited to completion throughput. Workers remain running through all phases. Each case drains fully before cohort latencies are calculated, including tasks that finish after their arrival phase.

A benchmark-only UNLOGGED trace adds one row insert and ready/claim/finish timestamp updates per completed task. This equal overhead is included in both versions' database CPU and top-level SQL time. Transition timestamps are inside transactions, not commit-visibility timestamps; ready-to-claim includes any remaining admission transaction. Claim-to-handler uses client/database clocks and is retained with that limitation. Due-to-ready includes waiting for finite quotas and serial ownership, not just scheduler delay. This is not a direct measure of runnable starvation.

Phase sampling wakes at the next deadline instead of allowing a coarse 250 ms tick to cross it. Boundary lag and snapshot collection duration are retained because snapshots are not instantaneous. Phase windows report observed completions during an interval; phase cohorts report every task scheduled in that phase. The separate five-second idle fixture remains the cleaner isolated idle-cost measurement. The coarse-sampling pair, pre-whole-task-deficit measurements and contaminated baseline are preserved and excluded below.

All tables show medians of three runs unless a range is stated. These are descriptive measurements, not confidence intervals or production capacity limits.

## Dynamic resource use and drain time

Total elapsed includes scheduled idle periods and final drain. For flow, imposed arrival rate can hold elapsed constant even when database work changes.

| Scenario | Elapsed seconds before → after | DB CPU seconds before → after | CPU change | DB CPU ms/completion before → after |
| --- | ---: | ---: | ---: | ---: |
| flow | 26.31 → 26.32 | 14.266 → 14.726 | +3.2% | 0.797 → 0.823 |
| mixed | 39.10 → 39.87 | 5.850 → 5.035 | -13.9% | 2.611 → 2.248 |
| recovery | 12.32 → 12.31 | 0.505 → 0.415 | -17.9% | 5.740 → 4.711 |

| Scenario | Candidate calls before → after | Zero-result calls before → after | Observation calls before → after |
| --- | ---: | ---: | ---: |
| flow | 915 → 1058 | 437 → 621 | 397 → 409 |
| mixed | 7480 → 4155 | 5887 → 2815 | 753 → 773 |
| recovery | 497 → 43 | 489 → 36 | 172 → 156 |

Observed DB CPU-second ranges: flow: 14.178–14.463 → 13.582–19.669; mixed: 5.706–6.040 → 4.906–5.045; recovery: 0.499–0.536 → 0.404–0.437.

Producer maximum-lag medians (before → after): flow: 158.4 → 248.8 ms; mixed: 38.0 → 41.7 ms; recovery: 26.0 → 27.3 ms. The slowest new flow run reaches 598.0 ms delivery lag; intended-arrival latency retains this delay rather than treating late insertion as on-time input.

## Latency, fairness and recovery

These are medians of each run's p95, not a pooled p95. End-to-end is planned arrival through database completion, including the handler duration and constrained-resource waiting.

| Scenario / cohort | Due → ready p95 ms before → after | Ready → claim p95 ms before → after | End-to-end p95 ms before → after |
| --- | ---: | ---: | ---: |
| flow / all | 119.5 → 124.6 | 114.4 → 133.4 | 204.6 → 224.4 |
| flow / phase:burst | 119.8 → 124.9 | 114.1 → 151.7 | 195.8 → 224.3 |
| mixed / all | 17700.6 → 18370.5 | 17.0 → 18.2 | 17827.7 → 18498.6 |
| mixed / kind:short | 26.0 → 40.8 | 16.1 → 18.0 | 232.4 → 244.5 |
| mixed / kind:hot | 18250.7 → 18912.4 | 17.2 → 18.7 | 18467.7 → 19108.6 |
| mixed / kind:serial | 1598.8 → 1877.6 | 17.0 → 17.5 | 1812.1 → 2095.6 |
| recovery / kind:new-arrival | 37.5 → 123.8 | 95.7 → 92.3 | 171.2 → 238.4 |

| Recovery event | Before median and range ms | After median and range ms |
| --- | ---: | ---: |
| First released slot → first waiter ready | 8.7 (5.7–11.3) | 43.8 (19.8–75.0) |
| Quota enable commit → first ready | 10.4 (5.6–26.5) | 2.3 (2.1–22.6) |

Release recovery measures database timestamps. Quota recovery begins immediately after the configuration call returns and can have tiny client/DB timing error. These are three observations per event, not percentiles or guarantees. The 100 ms maximum retry delay does not bound end-to-end scheduling latency: observation, SQL execution, commit and Worker polling also take time.

## Selected dynamic windows

| Scenario / phase | Completed/s before → after | DB CPU seconds before → after | Empty calls before → after |
| --- | ---: | ---: | ---: |
| flow / low | 49.5 → 49.0 | 0.358 → 0.601 | 87 → 104 |
| flow / high | 1482.5 → 1468.8 | 7.101 → 7.236 | 131 → 180 |
| flow / fall | 124.5 → 145.5 | 0.563 → 0.566 | 86 → 106 |
| flow / burst | 2380.6 → 2374.9 | 4.935 → 5.429 | 54 → 74 |
| mixed / pressure | 127.5 → 126.7 | 1.727 → 1.491 | 1312 → 718 |
| mixed / idle-again | 32.3 → 32.0 | 0.424 → 0.311 | 520 → 213 |
| recovery / held | 10.0 → 10.0 | 0.293 → 0.193 | 479 → 33 |
| recovery / idle | 0.0 → 0.0 | 0.045 → 0.042 | 2 → 0 |

Phase CPU/SQL snapshots can extend into the next nominal arrival phase during collection; they are approximate and cannot precisely attribute cost at a transition. Use whole-case CPU and the isolated idle fixture for resource conclusions.

Before maximum measured boundary lag: 3.9 ms; maximum snapshot collection: 147.2 ms. Per-window raw values are retained.

After maximum measured boundary lag: 3.0 ms; maximum snapshot collection: 155.1 ms. Per-window raw values are retained.

## Existing short fixtures

Each busy case still runs 2,000 tasks with a 20 ms handler, at 100/200 Worker slots. Serial history has 100,000 terminal rows and 256 keys; finite quotas are global 32 plus eight groups of 8. These compare with the previous durable-ready controller, not the pre-ready framework.

| Scenario / slots | Tasks/s before → after | Change | DB CPU change | Empty candidate calls before → after |
| --- | ---: | ---: | ---: | ---: |
| untagged/100 | 1860.8 → 2123.8 | +14.1% | -9.3% | 2 → 1 |
| untagged/200 | 2482.4 → 2212.3 | -10.9% | +14.1% | 1 → 0 |
| limited_shared/100 | 772.7 → 575.0 | -25.6% | -3.7% | 344 → 194 |
| limited_shared/200 | 784.0 → 592.2 | -24.5% | -6.2% | 336 → 192 |
| serial_history/100 | 1674.9 → 1631.3 | -2.6% | +1.9% | 21 → 14 |
| serial_history/200 | 1831.2 → 1771.6 | -3.3% | +3.2% | 13 → 9 |

Short-fixture throughput ranges (tasks/s): untagged/100: 1835.5–2109.8 → 1661.4–2142.0; untagged/200: 2135.3–2485.5 → 2174.8–2316.8; limited_shared/100: 772.3–773.0 → 572.1–635.6; limited_shared/200: 777.4–786.5 → 551.0–619.4; serial_history/100: 1642.2–1803.3 → 1628.2–1802.5; serial_history/200: 1811.3–1967.3 → 1461.3–1819.7.

Five-second idle CPU: **0.0866 → 0.0932 seconds**. Candidate calls: **0 → 0**; probes: **62 → 61**. First-completion median: **164.2 → 180.8 ms**, observed ranges **140.6–170.3 → 145.4–286.2 ms**. These six fixed-phase idle arrivals are not a latency percentile.

## Validation and reproducibility

- All taskcore race unit tests pass on the initial policy; the full Worker race suite is rerun on final `f0bb64e`. Demand tests cover bootstrap, small-quota pause, forecast-driven refill without another probe, group isolation, idle/invalidation, long-pause duration overflow, fractional deficits and independent retry state.
- The final PostgreSQL 17 ready race smoke suite passes, including a new held-quota regression: no more than 20 candidate calls over 800 ms of zero output, then ready recovery within 500 ms after finalization without another consumption event. Existing one-slot progress, durable-state, scheduler fencing and maintenance tests pass. The earlier fixed-reserve assertion was updated to test depletion/recovery under the new semantics; its failed run is retained.
- All three new workload fixtures pass race validation; the final deadline-aligned sampler is additionally validated with recovery. No fresh chaos fault-injection run is performed for this policy iteration. The earlier 200-round result belongs to the prior protocol revision, not this controller change.

- Before formal acceptance: 96,684 busy/dynamic completions and 96,684 attempts, plus 300 idle completions/300 attempts; remaining permits 0, lease errors/loss 0/0, recorded operation errors `{}`. Shutdown cancellation rollback logs retained: 5.
- After formal acceptance: 96,684 busy/dynamic completions and 96,684 attempts, plus 300 idle completions/300 attempts; remaining permits 0, lease errors/loss 0/0, recorded operation errors `{}`. Shutdown cancellation rollback logs retained: 2.

Local raw evidence: `/tmp/anclax-consumption-backoff-evidence-2026-09-16.tar.gz` (642,395 bytes, 97 files), SHA-256 `7ce8912c0e9d9d2d6ecf32b68f51834b8639edbb226e3ec4efb43dabf4721b3f`. Internal hashes and archive contents are verified. No new raw JSON is committed. The archive contains exact runner/environment, binary/harness hashes, all six runs, full ranges/cohorts/windows, validation logs, the excluded pilots and the source patch.

To reproduce, compile both revisions with the identical final benchmark/support files. Enable `ANCLAX_ARRIVAL_BENCH=1` and select `ANCLAX_ARRIVAL_BENCH_SCENARIOS=flow,mixed,recovery`; run `TestTaskPrefetchArrivalBenchmark` with `GOMAXPROCS=2`. The archived runner also includes the short and idle fixtures, exact environment and recorded order. `ANCLAX_TEST_REPORT_DIR` retains JSON reports.

Remaining coverage gaps include heterogeneous routing/strict/weight combinations, separate application processes/pools, sustained background business queries, longer MVCC/autovacuum aging and production arrival/duration distributions. Multi-Worker here means two runtimes in one process with a shared pool. More groups and deep stock can still make observation expensive. The benchmark demonstrates this controller's behavior under the stated load; it cannot establish production capacity or guarantee that prefetch always stays ahead.
