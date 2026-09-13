# 异步任务测试覆盖

[English](async-task-testing.md) | 中文

测试分层验证任务状态和 tag 并发约束。Mock 单测通过，只能证明模块在设定的依赖行为下处理正确；数据库锁、生成的 SQL、执行器进程和故障恢复需要分别验证。

| 层次 | 验证内容 |
| --- | --- |
| Mock 单测与可控时间 | 错误和取消传播、维护失败时停止领取、合并并发维护、失败后立即重试、冷却后恢复，以及阻塞领取提交等待标记。已有测试覆盖调度决策、本地容量、panic、停机和控制操作。 |
| PostgreSQL smoke | 多 tag 原子准入、动态上限、生命周期、旧租约、回滚、修改 tag 和映射清理。五种领取方式分别结合普通/serial 任务，形成十组测试，并检查 labels 和定时条件。 |
| 确定性容器故障 | 通过执行器阻塞信号，确认任务真正开始运行后才注入故障。检查满额等待不消耗 attempts 或部分名额、普通任务继续执行、释放后唤醒，以及指定任务在进程死亡或数据库断联后被接管。 |
| 随机容器 chaos | 混合受限和普通任务、暂停/取消/恢复、Worker 替换、数据库重启和控制面中断；跨重启持久化每次 tag 计数增加，检查超限和计数偏差。 |
| 持续负载与迁移 | 不同 tag 分布下的吞吐与领取 p95/p99、持续补充的队列、大量阻塞任务积压，以及历史迁移正确性和实际 DDL 锁等待。 |

确定性故障任务使用 `TAG-*` 名称，报告包含 `assert.tag_takeover` 和 `assert.tag_wait_release` 事件。原有任务统计仍描述 `LONG-*` 批量任务及初始化探针。接管断言检查被中断任务的 ID、owner 和 lease version，初始化重试不能满足条件。审计要求全局/分组峰值确实达到 3/2，且零超限、permit/计数一致、终态映射无残留。

断联测试只关闭某个 Worker 的已有数据库连接，并拒绝新连接，执行器的阻塞信号服务仍可访问。该场景调长原 Worker 的心跳间隔，单独验证续租失败导致的租约过期；进程必须保持存活，并在网络恢复后重新连接。提交旧 owner 的结果不得释放新尝试的名额。心跳失败引发停机的路径由已有测试验证。

## 常规运行

```bash
make test
make chaos-smoke  # 确定性场景 + 10 轮随机故障
ANCLAX_TASKCORE_CHAOS_SEED=8675309 make chaos  # 200 轮随机故障
```

`make test` 顺序执行 unit/race、确定性 runtime、数据库 smoke/stress 和短容器 chaos。这些 Make 入口要求 Docker 可用；直接运行 smoke 时设置 `ANCLAX_REQUIRE_DOCKER=1`，也会在缺少 Docker 时失败，避免跳过后误认为有覆盖。

通过 `ANCLAX_SMOKE_POSTGRES_IMAGE` 和 `ANCLAX_TASKCORE_CHAOS_POSTGRES_IMAGE` 选择数据库镜像。`.github/workflows/taskcore-tests.yml` 为 PR 和 main push 配置 PostgreSQL 15/17 检查；夜间及手动运行分别使用 seed 424242、8675309，并保留日志、报告和失败诊断。合并前是否强制这些检查仍由仓库分支保护设置决定，新增 workflow 不会修改该设置。

## 性能与迁移

```bash
ANCLAX_TEST_REPORT_DIR=/tmp/anclax-reports make taskcore-perf

ANCLAX_TAG_LOAD_SECONDS=10 \
ANCLAX_TAG_MIGRATION_HISTORY_COUNT=1000000 \
ANCLAX_TEST_REPORT_DIR=/tmp/anclax-reports make taskcore-perf
```

负载测试每完成一个任务就补充一个，在固定时间内保持可领取任务。场景包括无 tag、共享热点 tag、大量 tag 组合，以及旁边积压大量冻结任务的混合负载。p95/p99 测量成功领取调用，包含维护和事务开销，不包含任务排队等待及执行器工作。空领取次数、两类任务完成数和总耗时单独记录；每个场景结束时名额和占用必须归零。

| 参数 | 默认值 |
| --- | --- |
| `ANCLAX_TAG_LOAD_SECONDS` | 每个场景 5 秒 |
| `ANCLAX_TAG_LOAD_WORKERS` | 16 |
| `ANCLAX_TAG_LOAD_TAGS` | 多 tag 场景的 1,000 组 tenant/resource |
| `ANCLAX_TAG_LOAD_BACKLOG` | 混合场景积压 20,000 个冻结任务 |
| `ANCLAX_TAG_MAX_CLAIM_P99_MS` | 500 ms |
| `ANCLAX_TAG_MIN_TASKS_PER_SECOND` | 每秒完成 10 个任务 |
| `ANCLAX_TAG_MIGRATION_HISTORY_COUNT` | 普通 smoke 为 10,000；`make taskcore-perf` 为 100,000；夜间为 1,000,000 |

上述阈值用于发现明显回归，不是生产 SLA；稳定的压测机器可以设置更严格的预算。设置 `ANCLAX_TEST_REPORT_DIR` 后，结果写入 `tag-load.json` 和 `tag-migration.json`。

迁移测试先让旧版本上的读事务持锁，确认迁移在等待 DDL 锁，再释放读事务，验证升级、回滚及原始历史 tag 查询。迁移计时从数据准备完成后开始，包含人为制造的锁等待，不包含插入历史数据的时间。生产表的数据量、payload、索引和竞争事务仍需要有代表性的测量。

Mock 可以系统列举模块约定的分支、边界条件和依赖返回值，但无法穷尽无限的状态组合，也无法证明真实依赖符合 mock 假设。状态机和性质测试可以探索更多操作顺序；数据库集成测试和进程故障测试负责验证上述真实边界。
