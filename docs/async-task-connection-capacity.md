# Async task connection capacity

> Historical benchmark report. These benchmark harnesses have been retired; current capacity testing uses the [sustained scheduling benchmark](scheduling-capacity-benchmark.md). Reproduction commands below apply to the recorded revisions and archived harnesses, available in the [source snapshot](https://github.com/cloudcarver/anclax/tree/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/pkg/taskcore/e2e). Raw result links point to Git history; generated JSON/log reports are no longer stored in the current tree.

This benchmark measures sustained execution with constrained business and lease-renewal pools. It is an opt-in PostgreSQL test, separate from the tag-admission throughput benchmark.

## Measurements on 2026-09-14

Measured on an Apple M5 development machine with 10 logical CPUs and 16 GiB host RAM. Docker had 10 CPUs and approximately 7.75 GiB RAM; PostgreSQL was 15.19 and the Go toolchain was 1.26.5 on arm64. Suites ran sequentially; existing background services remained running. [Raw measurements and exact environment overrides](https://github.com/cloudcarver/anclax/blob/a6b3869ce43c996d424e3bbc9e3b6c7c851f6e01/docs/benchmarks/async-task-connection-capacity-2026-09-14.json) include every completed capacity case.

| Workload | Business + renewal connection limits | Highest tested passing concurrency | Tested failure | Observation |
| --- | --- | ---: | --- | --- |
| Idle handlers | 1 + 1 | 10,000 | 12,500: lost leases; 15,000 also failed | 10,000 also passed a 60-second observation |
| Idle handlers | 2 + 2 | 10,000 | Not reached within this series | 20 seconds at target concurrency |
| Idle handlers | 10 + 10 | 50,000 | Not reached within this series | 20 seconds at target concurrency; all 50,000 completed once |
| Database transactions | 1 + 1 | 30 | 35: transaction p99 297.6 ms | Passing p99 217.8 ms |
| Database transactions | 2 + 2 | 65 | 75: transaction p99 315.3 ms | Passing p99 206.9 ms |
| Database transactions | 10 + 10 | 350 | 400: transaction p99 337.0 ms | Passing p99 59.9 ms |

The database workload means **four transactions per second per task**, each holding a connection for at least five milliseconds. Its boundary is a throughput/latency objective, not the number of goroutines that can remain alive. Every database-workload failure above retained healthy renewal during the observation window.

The one-connection idle failure demonstrates a different limit: renewing 12,500 attempts through one connection could not keep every attempt within its lease deadline on this setup. Executors were interrupted with `task lock lost`. The 10,000-task 60-second run reported no renewal errors or lost leases. With 10 + 10 connections, the highest tested 50,000-task case had no renewal errors/losses and reached both configured pool limits; it does not establish 50,000 as a maximum.

Startup phases influence transaction timing and database plans, so latency need not increase monotonically between passing levels. Use the recorded pass/fail brackets and workload definition rather than extrapolating a fixed tasks-per-connection ratio.

Validation accompanying these measurements passed: the complete unit suite with `-race -tags=ut`, PostgreSQL task-store/lifecycle/tag-concurrency regression and migration tests, the dedicated-pool exhaustion test, and the containerized chaos smoke suite (seed 424242, 10 iterations). The chaos run covered owner termination and a database partition while its owner process remained alive, and finished with 55 completed and four cancelled workload tasks.

## Reproduce

```sh
ANCLAX_TEST_REPORT_DIR=/tmp/anclax-capacity make taskcore-capacity
```

Docker must be available. The suite creates its own PostgreSQL container on port 5499 and removes it afterwards. Do not run it concurrently with another Anclax smoke suite using that port. Reports are written to `async-connection-capacity.json` in the requested directory.

Optional environment variables:

| Variable | Default | Meaning |
| --- | --- | --- |
| `ANCLAX_ASYNC_CAPACITY_POOLS` | `1,2,10` | Each case uses this limit separately for the business pool and renewal pool. `1` means at most one connection in each pool, two total for the tested model. |
| `ANCLAX_ASYNC_CAPACITY_MODES` | `idle,database` | Workloads to test. |
| `ANCLAX_ASYNC_CAPACITY_IDLE_LEVELS` | `100,1000,5000,10000` | Concurrent handler counts for the idle workload. |
| `ANCLAX_ASYNC_CAPACITY_DB_LEVELS` | `10,25,50,100,250,500,1000` | Concurrent handler counts for the database workload. |
| `ANCLAX_ASYNC_CAPACITY_SECONDS` | `20` | Observation after all handlers start; minimum 20 seconds. |
| `ANCLAX_SMOKE_POSTGRES_IMAGE` | `postgres:15` | Database image. |

Levels are tested in ascending order. The first failing level stops that pool/workload series, producing a passing/failing bracket. If every level passes, the result is a measured lower bound on capacity, not a discovered maximum.

## Workloads and acceptance criteria

Both workloads use persisted tasks, real Engine admission, `Runtime.RunTask`, lease renewals, normal finalization, and one listener subscription per task. Tasks are admitted gradually by ID. Finalization drains in groups of 16. This measures sustained handler capacity without conflating it with a burst of queued claims or simultaneous completions. It does not measure automatic polling throughput, worker-registry heartbeat contention, tag/serial admission, or external services.

- **Idle:** handlers wait on a channel. Business database traffic comes from framework operations and listener polling. This models long tasks spending most of their time outside PostgreSQL; it is not a CPU-bound workload.
- **Database:** each handler attempts four transactions per second. Each transaction executes `SELECT pg_sleep(0.005)` and therefore occupies a business connection for at least five milliseconds. Each transaction has a one-second deadline.

The worker uses the default three-second renewal interval and nine-second lease TTL. All target handlers must stay running through the observation period, spanning more than two lease TTLs. Renewal errors or lost leases fail the case. Afterwards all tasks must complete with exactly one attempt and the listener must report completion.

The database workload must additionally deliver at least 90% of its requested transaction rate and keep transaction p99 at or below 250 milliseconds. A task merely remaining alive while its database calls queue does not meet this workload's capacity criterion.

The report includes achieved concurrent starts, successful completions, startup/drain times, sampled peak connections, pool waits, renewal query counts/errors/losses, average renewal batch size, renewal latency, and business transaction rate/p99. Renewal p99 is an upper histogram bucket boundary; business p99 is calculated from recorded transaction samples. Setup and observer connections are outside the tested model's pool limits.

## Configuration and interpretation

```yaml
worker:
  concurrency: 1000
  leaseRenewalMaxConnections: 10
```

`worker.concurrency` still limits handlers through finalization. Waiting inside a handler retains that slot. The renewal limit is separate from the business limit configured through `LibConfig.Pg.MaxConnections`. Increasing concurrency does not reserve one PostgreSQL connection per task, but business query load and the number of rows renewed still grow with task count.

Use results for a matching workload, database host, network, and latency objective. They do not establish a universal production maximum. A short passing observation should be followed by a longer soak and representative business handlers before choosing a production limit.
