# Async task test coverage

English | [中文](async-task-testing.zh.md)

The suite checks task and tag-concurrency invariants at several layers. Passing mock tests alone does not establish that PostgreSQL locking, generated queries, executor processes or recovery behave like the mock.

| Layer | What it checks |
| --- | --- |
| Unit tests with mocks and controlled time | Error/cancellation propagation, maintenance failure preventing admission, coalescing concurrent sweeps, retry after failure, cooldown expiry, and committing blocked-claim markers. Existing tests cover engine decisions, local capacity, panic handling, shutdown and control-plane behavior. |
| PostgreSQL smoke | Atomic multi-tag admission, dynamic limits, lifecycle outcomes, stale leases, rollback, tag edits, and membership cleanup. Ten combinations cover five claim paths with/without serial ordering, plus labels and scheduling. |
| Deterministic container faults | An executor gate proves that the target handler is running before a fault. Tests observe full-tag waiting without consuming attempts or partial permits, unlimited progress, and execution after release. Specific tasks must be reclaimed after owner death and an isolated database partition. |
| Random container chaos | Mixed limited/unlimited tasks, pause/cancel/resume, worker replacement, database restarts and control-plane outages. Durable per-tag observations detect excess admission and counter drift across restarts. |
| Sustained load and migration | Throughput and successful-claim p95/p99 under several tag distributions; a continuously replenished queue and an existing blocked backlog; historical migration correctness and a real DDL lock wait. |

The deterministic fault fixtures use `TAG-*` names and emit `assert.tag_takeover`/`assert.tag_wait_release` report events. The existing task summary covers `LONG-*` workload tasks, including the initialization probes. Recovery assertions target the interrupted task ID, owner and lease version; initialization retries cannot satisfy them. The audit requires global/group peaks of 3/2, no oversubscription, consistent permits/counters and no terminal membership residue.

The database-partition test closes existing TCP connections and rejects new ones for one worker. Its executor gate stays reachable. The owner's heartbeat interval is extended for this fixture so lease-renewal expiry is tested independently of heartbeat shutdown; the original process must remain alive and reconnect after healing. Replacement attempts must keep their permits when an old-owner result is submitted.

## Regular runs

```bash
go run ./cmd/anclax install  # first checkout or after changing tool versions
make test
make chaos-smoke  # deterministic scenarios + 10 random iterations
ANCLAX_TASKCORE_CHAOS_SEED=8675309 make chaos  # 200 random iterations
```

`make test` runs unit/race tests, deterministic runtime scenarios, PostgreSQL smoke/stress and short container chaos sequentially. Docker is mandatory for these Make targets. `ANCLAX_REQUIRE_DOCKER=1` also makes direct smoke invocations fail instead of skip when Docker is unavailable.

Use `ANCLAX_SMOKE_POSTGRES_IMAGE` and `ANCLAX_TASKCORE_CHAOS_POSTGRES_IMAGE` to select database images. `.github/workflows/taskcore-tests.yml` runs regular checks on PostgreSQL 15 and 17 for PRs and main pushes. Nightly/manual runs use seeds 424242 and 8675309 for each database version and preserve logs, reports and failure diagnostics. Branch-protection requirements remain repository settings; adding a workflow does not make its checks mandatory for merging.

## Performance and migration runs

```bash
ANCLAX_TEST_REPORT_DIR=/tmp/anclax-reports make taskcore-perf

ANCLAX_TAG_LOAD_SECONDS=10 \
ANCLAX_TAG_MIGRATION_HISTORY_COUNT=1000000 \
ANCLAX_TEST_REPORT_DIR=/tmp/anclax-reports make taskcore-perf
```

The load test replenishes each completed task for a fixed duration, keeping the runnable queue populated. Cases cover untagged tasks, shared hot tags, many tag pairs, and mixed traffic beside a frozen backlog. Claim percentiles measure successful database claim calls, including their maintenance and transaction work; they exclude time spent waiting in the queue and executor work. Empty claims, completed tasks by cohort and elapsed time are reported separately. Permits and usage must drain before each case finishes.

| Setting | Default |
| --- | --- |
| `ANCLAX_TAG_LOAD_SECONDS` | 5 seconds per case |
| `ANCLAX_TAG_LOAD_WORKERS` | 16 |
| `ANCLAX_TAG_LOAD_TAGS` | 1,000 distinct tenant/resource pairs in the many-tag case |
| `ANCLAX_TAG_LOAD_BACKLOG` | 20,000 frozen tasks in the mixed case |
| `ANCLAX_TAG_MAX_CLAIM_P99_MS` | 500 ms |
| `ANCLAX_TAG_MIN_TASKS_PER_SECOND` | 10 completed tasks/s |
| `ANCLAX_TAG_MIGRATION_HISTORY_COUNT` | 10,000 in ordinary smoke; 100,000 through `make taskcore-perf`; 1,000,000 nightly |

These are broad regression budgets for the test environment, not production SLAs. Set tighter budgets on a stable benchmark host. JSON reports are written to `ANCLAX_TEST_REPORT_DIR` as `tag-load.json` and `tag-migration.json`.

The migration fixture holds a reader transaction against the old schema, observes migration waiting for its DDL lock, releases the reader, then checks upgrade/rollback and original historical tag queries. Recorded migration time starts after fixture insertion and includes the controlled lock wait; it does not include seeding time. Production table sizes, payloads, indexes and competing transactions need representative measurement.

Mock tests can systematically enumerate a module's specified cases and dependency responses. They cannot enumerate an unbounded state space or prove that real dependencies follow the assumed contract. State-machine/property tests can explore more sequences; real-database and process tests remain necessary for the boundaries above.
