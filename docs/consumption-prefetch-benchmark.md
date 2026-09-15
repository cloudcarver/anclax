# Consumption-paced prefetch measurements

2026-09-15. Consumption pacing substantially reduces idle scheduler work, but this version does **not** establish a consistent improvement in busy throughput. Eight of fourteen scenario/concurrency medians are lower. Finite shared quotas regress in all three runs at both concurrency levels. PR #72 remains a draft; this is local evidence, not a production capacity result.

## Scope and method

- Before: `5635ba33219f5fead8ad98f38024069aca11c66b`, the long-lived scheduler with immediate productive loops and a 20 ms empty wait. After: `6b110f720fbcb87f8e40c1e4b9be48f7b6247691`; production sources are `8315f4e6d7533f9b9d4cdc2c5b0cc62eb3c64fb3`, followed only by an idle benchmark.
- Both use ready reservations, bulk simple admission and two control slots. The change adds committed per-Worker consumption counters, bounded refill credit, rate/burst pacing and idle probes/backoff. This is a comparison against the previous long-task version, not against main.
- Identical benchmark files in both worktrees; PostgreSQL 17 on 2 CPU / 1 GiB, Apple M5 host, Go 1.26.5 / `GOMAXPROCS=2`, main pool 50, renewal pool 10. Automatic Worker execution, 100/200 slots, claim batch 32, 2,000 runnable tasks and 20 ms handlers per case. Serial history: 100,000 rows; blocked cases: one full quota-zero group with higher weight.
- Three alternating pairs: before-1, after-1, after-2, before-2, before-3, after-3. Timed benchmarks ran after chaos and scenario suites exited; the existing unrelated `nile-k0s` container remained. Each table entry is a median of three runs. These short synthetic repetitions do not establish confidence intervals.
- PostgreSQL CPU includes all database execution during the run. Total SQL time sums top-level `pg_stat_statements`, avoiding nested double counting. Counter writes are inside claim SQL; the added counter reads are included in total SQL/CPU and reported separately. Enqueue time/CPU, pool observations, errors and latency percentiles are retained in the local raw evidence.

## Idle cost and arrival delay

One 100-slot Worker warms up for two seconds, then stays empty for five seconds. A burst of 100 tasks follows. The first-completion measurement includes a 20 ms handler and is one fixed-phase arrival per run, not a latency percentile.

| Five-second idle window / subsequent arrival | Before | After |
| --- | ---: | ---: |
| Prefetch SQL calls | 231 | 21 |
| Additional consumption reads | 0 | 21 |
| PostgreSQL CPU seconds | 0.1443 | 0.0883 |
| Total top-level SQL milliseconds | 52.49 | 18.78 |
| First completed task, ms | 68.6 | 259.8 |
| Drain 100 tasks, ms | 180.3 | 405.4 |

Prefetch calls fall 90.9%; including the new reads, scheduler queries fall 81.8%. Idle database CPU falls 38.8%, an absolute saving of about 0.011 CPU core in this fixture. Ordinary empty Worker claims remain essentially unchanged (258 versus 257). First-completion observations range from 61–196 ms before and 245–287 ms after. The longer polling interval has a visible arrival-latency cost.

## Busy throughput and database cost

CPU change compares CPU seconds for the same 2,000 completed tasks. Prefetch calls count attempts, including empty results; the SQL returns one result row even when it prepares zero tasks, so its result-row count is not admission throughput.

| Scenario | Slots | Completed/s before → after | Change | PG CPU change | Prefetch calls before → after | New counter reads |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| untagged | 100 | 2046.1 → 2133.8 | +4.3% | -2.9% | 80 → 70 | 87 |
| untagged | 200 | 2398.6 → 2492.3 | +3.9% | -6.9% | 50 → 31 | 32 |
| unlimited_shared | 100 | 2157.9 → 2254.4 | +4.5% | -6.4% | 89 → 79 | 95 |
| unlimited_shared | 200 | 2577.3 → 2473.8 | -4.0% | +3.7% | 53 → 32 | 33 |
| limited_shared | 100 | 597.7 → 563.8 | -5.7% | +1.5% | 188 → 142 | 142 |
| limited_shared | 200 | 603.1 → 566.5 | -6.1% | -3.4% | 188 → 138 | 138 |
| serial_history | 100 | 1666.6 → 1867.2 | +12.0% | -3.2% | 53 → 51 | 55 |
| serial_history | 200 | 2033.1 → 2023.5 | -0.5% | +0.3% | 31 → 26 | 27 |
| blocked_0 | 100 | 2315.7 → 2299.3 | -0.7% | +2.1% | 85 → 81 | 99 |
| blocked_0 | 200 | 2356.7 → 2392.5 | +1.5% | -7.6% | 49 → 31 | 31 |
| blocked_20000 | 100 | 2376.2 → 2134.9 | -10.2% | +14.4% | 85 → 67 | 80 |
| blocked_20000 | 200 | 2227.6 → 2044.8 | -8.2% | +6.9% | 58 → 28 | 30 |
| blocked_200000 | 100 | 1737.3 → 1520.3 | -12.5% | +11.6% | 79 → 55 | 65 |
| blocked_200000 | 200 | 1665.0 → 1680.0 | +0.9% | -2.9% | 50 → 26 | 30 |

Untagged/serial gains and several regressions have overlapping three-run ranges. The finite-quota result is clearer in this sample: at 100 slots the ranges are 593.9–613.0/s before and 562.9–569.9/s after; at 200 slots, 591.4–604.0/s versus 556.4–566.7/s. Fewer prefetch calls alone therefore do not establish better scheduling efficiency. The experiment combines pacing with demand-accounting writes; it does not isolate each change's CPU contribution.

## Feedback safeguard

An initial fixed `32 / consumption_rate` cadence fed scheduling delays back into the measured rate. A 20,000-task finite-quota pilot dropped from 623.2/s to 201.4/s as the run continued. Adapting the interval target to half the observed nonzero consumption burst restored 592.7/s in the follow-up pilot (1,905 → 1,352 prefetch calls; 17.178 → 16.952 PG CPU seconds versus before). These single pilot comparisons are excluded from the final medians. A deterministic one-slot regression and a real PostgreSQL 100-Worker-slot / one-tag-slot test now prevent the particularly severe small-quota self-throttling case.

## Correctness and limits

- Full race unit suite passed. PostgreSQL 15/17 passed ready ownership, counter atomicity/rollback, cross-Worker independence, unlimited/limited tags, serial/weighted scheduling, migration, candidate-plan, renewal and lifecycle tests. The PG17 small-quota test also passed separately. Three complete DST scenario repeats passed with the previously corrected assertion propagation.
- Fresh **200 chaos rounds** on production commit `8315f4e`: 864 submitted, 860 completed, 4 cancelled and 4 retried; zero pending/ready/running/failed business tasks at drain. There were 21 Worker disruptions, 13 database restarts, 8 control-plane outages and 103 configuration updates. The tag audit recorded 593 observations, zero violations and no remaining permits/terminal memberships. Both chaos binaries identify the same clean source commit.
- All 84 busy benchmark cases passed: 168,000 completions and attempts, zero repeated tasks, renewal errors/loss, remaining permits or recorded operation SQLSTATE errors. Six idle tests each completed another 100 tasks exactly once and retained the same scheduler lease version. Shutdown connection-close rollback logs remain in both versions (11 before / 12 after); they are not hidden or counted as in-window operation errors.
- The existing ready cap and PostgreSQL reservation protocol remain authoritative. Startup and periodic probes are still necessary when consumption is zero. High Worker churn, many resource/routing groups and production workloads require separate measurement.

Keep the current pacing change in draft: idle backoff has a measured benefit, but the busy-workload regressions and increased arrival delay need to be addressed before calling it a general performance improvement.

## Evidence

The new raw JSON and logs are stored locally rather than added to this PR. The archive includes all six paired runs, idle results, metadata/binary/harness hashes, validation and chaos reports, the pilot reports, source patch and comparison scripts. Existing historical benchmark reports remain unchanged.

Archive: `anclax-consumption-prefetch-evidence-2026-09-15.tar.gz`, 195074 bytes. SHA-256: `bbdc0fd5e4bb3ae32700d0c78acd1f1d09779550865260d415c37a8d6d2a21ef`. The local path is `/tmp/anclax-consumption-prefetch-evidence-2026-09-15.tar.gz`; it is not a remotely hosted artifact.
