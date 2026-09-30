# Supply-first prefetch measurements

> Historical benchmark report. These benchmark harnesses have been retired; current capacity testing uses the [sustained scheduling benchmark](scheduling-capacity-benchmark.md). Reproduction commands below apply to the recorded revisions and archived harnesses, available in the [source snapshot](https://github.com/cloudcarver/anclax/tree/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/pkg/taskcore/e2e). Raw result links point to Git history; generated JSON/log reports are no longer stored in the current tree.

2026-09-15. The scheduler now favors available ready work: productive batches continue immediately, and historical consumption no longer limits refill. Finite-quota throughput improves, with additional database work; the measurements do **not** establish an overall throughput or idle-arrival improvement. The comparison below includes both the previous consumption-paced version and the original long-lived scheduler. These are local synthetic measurements, not production capacity limits.

## Policy and comparison

| Version | Production source | Policy |
| --- | --- | --- |
| Original | `5635ba33219f5fead8ad98f38024069aca11c66b` | Immediate productive batches; 20 ms after empty admission |
| Consumption | `db96267a5865a137df5a76c9c54920e11cec443e` | Consumption credit/rate pacing; idle probing up to 250 ms |
| Supply | `c44d157432c66afdca42b5a13479d64f47f17d70` | Immediate productive batches; full ready window 20 ms; active blocked work 5 ms; idle/quiescent 20→40→80→100 ms |

Each successful supply batch is followed immediately, with a maximum of 256 admissions per transaction. PostgreSQL still bounds ready reservations by live Worker capacity, capped globally at 4,096, and enforces finite tags, serial ownership, expiry and fencing. The per-Worker consumption column, read and claim-side accounting write are removed. A partial pending/due index supports the empty-work hint; its maintenance cost is included in enqueue measurements.

The distinction between full and blocked is deliberate: a full ready buffer already supplies Workers, whereas a blocked finite resource can become available on completion without another claim. Only genuine idle/quiescent outcomes accumulate backoff. These hints are conservative: unrelated long-running owners may keep unavailable work checking at 5 ms. The intervals are start-to-start policy targets, not task-latency guarantees. See [design and integration](ready-task-prefetch.md).

## Method

- PostgreSQL 17 Docker, 2 CPU / 1 GiB; Apple M5 host, Go 1.26.5 arm64 / `GOMAXPROCS=2`; main pool 50 and renewal pool 10. Existing host applications and the unrelated `nile-k0s` container remained running. Our builds, chaos and stress tests finished before timed benchmarks.
- Identical two benchmark source files in all three worktrees, with hashes and binary provenance in the local metadata. Historical worktrees contain harness-only modifications; production sources match the pinned commits.
- Three repetitions per version in balanced order: original, consumption, supply; consumption, supply, original; supply, original, consumption. Each version occupies every position once and has the same mean temporal position. Tables use medians; three runs do not establish confidence intervals.
- Each busy case automatically executes 2,000 tasks with 20 ms handlers, 100/200 Worker slots and claim batches of 32. Serial history has 100,000 rows / 256 keys. Finite shared quotas are global 32 and eight groups of 8. Blocked cases have one higher-weight quota-zero group plus the runnable tasks.
- CPU includes all PostgreSQL work during execution. SQL time sums top-level `pg_stat_statements`, avoiding nested double counting. Enqueue includes task inserts, group triggers and the new due index; migration, history loading and setup VACUUM are excluded. The experiment does not isolate each optimization's contribution.

## Busy throughput

CPU change is database CPU seconds for the same 2,000 completed tasks, relative to Consumption. Completion throughput, not claim count, is reported.

| Scenario | Slots | Original tasks/s | Consumption tasks/s | Supply tasks/s | Supply throughput change | Supply PG CPU change |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| untagged | 100 | 1611.8 | 1440.1 | 1425.8 | -1.0% | +1.0% |
| untagged | 200 | 1978.4 | 1524.0 | 1953.6 | +28.2% | -22.3% |
| unlimited_shared | 100 | 1601.8 | 1770.3 | 1657.0 | -6.4% | +6.3% |
| unlimited_shared | 200 | 1622.3 | 1480.5 | 1386.1 | -6.4% | +4.4% |
| limited_shared | 100 | 572.8 | 550.6 | 707.2 | +28.4% | +11.0% |
| limited_shared | 200 | 542.3 | 520.3 | 757.5 | +45.6% | +7.2% |
| serial_history | 100 | 1384.5 | 1200.3 | 1336.8 | +11.4% | -5.4% |
| serial_history | 200 | 1197.0 | 1494.5 | 1410.0 | -5.7% | +5.6% |
| blocked_0 | 100 | 1828.5 | 2001.4 | 1582.4 | -20.9% | +27.3% |
| blocked_0 | 200 | 1557.9 | 1835.0 | 1490.7 | -18.8% | +23.4% |
| blocked_20000 | 100 | 1340.5 | 1376.8 | 1628.2 | +18.3% | -12.0% |
| blocked_20000 | 200 | 1587.8 | 1625.9 | 1561.8 | -3.9% | +8.2% |
| blocked_200000 | 100 | 1055.3 | 1298.9 | 1190.8 | -8.3% | +12.8% |
| blocked_200000 | 200 | 1449.4 | 1117.2 | 1338.5 | +19.8% | -15.9% |

Finite-quota throughput improves 28.4% / 45.6% against Consumption and 23.5% / 39.7% against Original at 100/200 slots. The cost against Consumption is 11.0% / 7.2% more execution CPU. Across all fourteen combinations, eight throughput medians are lower than Consumption; changes range from -20.9% to +45.6%. This supports the supply preference for constrained work, not a general performance win.

Finite-quota throughput ranges, in original / consumption / supply order:

- 100 slots: 562.5–579.5/s / 448.7–558.0/s / 691.7–734.6/s.
- 200 slots: 511.0–578.8/s / 514.0–530.2/s / 673.7–769.1/s.

Other workloads have overlapping ranges and host variation; their median differences alone should not be read as guaranteed improvements. The local summary preserves every range and both baseline comparisons.

## Idle cost and arrivals

One 100-slot Worker warms up for two seconds, then remains empty for five seconds before 100 tasks arrive. First completion includes a 20 ms handler and is one fixed-phase arrival per run, not a latency percentile.

| Five-second idle / subsequent arrival | Original | Consumption | Supply |
| --- | ---: | ---: | ---: |
| Prefetch SQL calls | 228 | 21 | 52 |
| Additional consumption reads | 0 | 21 | 0 |
| PG CPU seconds | 0.2152 | 0.1445 | 0.1575 |
| Total top-level SQL milliseconds | 75.60 | 26.64 | 37.00 |
| First completed task, ms | 208.8 | 240.3 | 237.3 |
| Drain 100 tasks, ms | 435.6 | 550.4 | 635.4 |

First-completion ranges: original 145.6–222.0 ms; consumption 212.3–267.8 ms; supply 232.0–458.8 ms. Idle backoff still adds arrival delay compared with the original 20 ms loop; no notification mechanism was added.

Against Consumption, the first-completion median is essentially unchanged (240.3 → 237.3 ms), the drain median is higher, and idle CPU rises 9.0%. Against Original, idle CPU falls 26.8%. The shorter idle polling cap therefore does not establish a measured arrival-latency improvement in this fixture; database execution and Worker polling also contribute to completion time.

## Supply observations and added work

The existing 20 ms observer reads ready/running/pending counts together with completions. Startup and tail are excluded: at least one Worker-capacity worth of completions must have occurred, with at least that many tasks left. An empty-ready sample with pending work and fewer running tasks than Worker slots is an indicator, **not proof of runnable starvation**, especially under finite tag/serial constraints. Sampling can miss short gaps. Raw evidence records both empty-ready and this stricter indicator.

| Scenario | Slots | Supply prefetch calls | Zero prepared | Prepared tasks | Empty-ready samples, % | Pending + unused Worker slots + empty ready, % |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| untagged | 100 | 77 | 18 | 2000 | 4.7 | 2.3 |
| untagged | 200 | 50 | 8 | 2000 | 0.0 | 0.0 |
| unlimited_shared | 100 | 90 | 19 | 2000 | 2.8 | 0.0 |
| unlimited_shared | 200 | 47 | 8 | 2000 | 0.0 | 0.0 |
| limited_shared | 100 | 449 | 357 | 2000 | 71.8 | 71.8 |
| limited_shared | 200 | 451 | 314 | 2000 | 49.0 | 49.0 |
| serial_history | 100 | 54 | 11 | 2000 | 24.5 | 15.1 |
| serial_history | 200 | 33 | 6 | 2000 | 3.0 | 3.0 |
| blocked_0 | 100 | 83 | 19 | 2000 | 2.4 | 0.0 |
| blocked_0 | 200 | 47 | 5 | 2000 | 0.0 | 0.0 |
| blocked_20000 | 100 | 87 | 27 | 2000 | 2.9 | 2.9 |
| blocked_20000 | 200 | 53 | 11 | 2000 | 0.0 | 0.0 |
| blocked_200000 | 100 | 86 | 25 | 2000 | 2.7 | 0.0 |
| blocked_200000 | 200 | 54 | 14 | 2000 | 0.0 | 0.0 |

Prepared tasks use the SQL function's returned count, not its result-row count: an empty prefetch still returns one SQL row. Productive calls never incur pacing sleep. Finite resources can legitimately keep ready empty; prefetch cannot reserve a permit still held by an executing task.

The new due index has a write cost. Representative enqueue costs below include both runnable and blocked task insertion; execution CPU alone is not the total database cost of submitting and finishing a workload.

| Scenario | Slots | Consumption → Supply enqueue CPU, s | Consumption → Supply enqueue elapsed, s | Enqueue + execution CPU change |
| --- | ---: | ---: | ---: | ---: |
| untagged | 100 | 0.0546 → 0.0504 | 0.2192 → 0.1732 | +0.9% |
| untagged | 200 | 0.0806 → 0.0486 | 0.2010 → 0.1534 | -22.8% |
| limited_shared | 100 | 0.0738 → 0.0859 | 0.1827 → 0.1975 | +11.4% |
| limited_shared | 200 | 0.0859 → 0.0976 | 0.2168 → 0.2191 | +7.5% |
| blocked_200000 | 100 | 7.4059 → 6.9647 | 7.6689 → 7.3373 | +5.2% |
| blocked_200000 | 200 | 6.5599 → 6.9077 | 6.8791 → 7.3068 | -2.2% |

A preceding experiment checked both full and blocked windows every 5 ms. Its untagged-100 median was 1,767.0/s with 2.260 PG CPU seconds, despite no sampled supply gap. This motivated reducing full-window polling to 20 ms. That experiment and uncommitted pilots ran in a different window; they are preserved separately and excluded from all final medians above. The final comparison does not isolate the benefit of this interval adjustment. Final code received a fresh validation and 200-round chaos run after the change.

## Validation and limits

- Full race unit suite passed. PostgreSQL 15/17 passed ready lifecycle/ownership, expiry and stale fencing, finite/unlimited tag and quota activation, serial/weighted scheduling, migration, indexed candidate/due plans and batch renewal regressions. Supply tests cover productive/full/blocked/idle/quiescent decisions, idle renewal/cancellation and 100 Worker slots with a one-slot tag quota. Three complete DST scenario repeats passed with assertion propagation enabled.
- Fresh **200 chaos rounds** on clean `c44d157`: 864 submitted, 860 completed, 4 cancelled and 4 retried; zero pending/ready/running/failed business work at drain. There were 21 Worker disruptions, 13 database restarts, 8 control-plane outages and 103 configuration updates. Tag audit: 589 observations, zero violations and no terminal memberships or remaining permits. Both helper binaries identify the same clean final revision.
- All **126 busy cases passed**: **252,000 completions and attempts**, zero repeated tasks, renewal errors/loss, remaining permits or recorded operation SQLSTATE errors. Nine idle cases completed another 900 tasks exactly once and retained the scheduler lease version. Shutdown connection-close rollback logs remain in the evidence (original 18, consumption 10, supply 7); these are not counted as in-window operation errors.
- One additional **20,000-task** finite-quota Supply run at 100 slots completed at **614.9/s**, using **42.202 PG CPU seconds**, with zero repeated tasks, lease loss/errors or remaining permits. This is below the 707.2/s short-run median. There is no matched long-run baseline or within-run throughput series, so it establishes eventual drain, not stable throughput or a steady-state performance gain. It is excluded from comparative medians.
- The scheduler remains a singleton with its own throughput limit. Many distinct resource/routing groups, unrelated long-lived leases, very short tasks and Worker churn need separate measurement. Adequate supply is the policy preference; finite quotas, serial constraints, database saturation and the 20 ms full-window interval prevent a universal guarantee that ready never empties.
- Migration 16 still requires stopped Workers for upgrade/rollback. No production deployment or mixed-version operation was tested. This PR remains a draft.

## Evidence

New raw JSON/logs are local, not committed to the PR. The archive contains final nine-run comparison and steady follow-up, identical harnesses, binary/source metadata, fresh validation/chaos reports, scripts and source patch. The full earlier 5 ms experiment and pilot results have separate directories. Internal checksums and the archive contents were verified.

Archive: `anclax-supply-prefetch-evidence-2026-09-15.tar.gz`, 465765 bytes. SHA-256: `fd7de83c2b28673d246ae7b01a2d74158c906b6f2bad49e2c9492ad422355c63`. Local path: `/tmp/anclax-supply-prefetch-evidence-2026-09-15.tar.gz`; this is not a remotely hosted artifact. Historical [consumption](consumption-prefetch-benchmark.md), [long-task](long-task-prefetch-benchmark.md) and [initial ready](ready-task-prefetch-benchmark.md) comparisons remain separate experiments. Their ratios must not be multiplied into these results.
