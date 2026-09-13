# 异步任务测试覆盖

[English](async-task-testing.md) | 中文

测试分层验证任务状态和 tag 并发约束。Mock 单测通过，只能证明模块在设定的依赖行为下处理正确；数据库锁、生成的 SQL、执行器进程和故障恢复需要分别验证。

| 层次 | 验证内容 |
| --- | --- |
| Mock 单测与可控时间 | 错误和取消传播、维护失败时停止领取、合并并发维护、失败后立即重试、冷却后恢复，以及阻塞领取提交等待标记。已有测试覆盖调度决策、本地容量、panic、停机和控制操作。 |
| PostgreSQL smoke | 多 tag 原子准入、动态上限、生命周期、旧租约、回滚、修改 tag 和映射清理。五种领取方式分别结合普通/serial 任务，形成十组测试，并检查 labels 和定时条件。迟到的心跳 SQL 不得让已明确离线的 Worker 重新上线，显式启动注册仍可恢复在线。 |
| 确定性容器故障 | 通过执行器阻塞信号，确认任务真正开始运行后才注入故障。检查满额等待不消耗 attempts 或部分名额、普通任务继续执行、释放后唤醒，以及指定任务在进程死亡或数据库断联后被接管。 |
| 随机容器 chaos | 混合受限和普通任务、暂停/取消/恢复、Worker 替换、数据库重启和控制面中断；跨重启持久化每次 tag 计数增加，检查超限和计数偏差。 |
| 持续负载 | 不同 tag 分布下的吞吐与领取 p95/p99、持续补充的队列，以及大量阻塞任务积压。 |
| 迁移兼容性 | 固定 10,000 条历史数据，检查升级、回滚、任务数据保留和原始 tag 查询。 |

确定性故障任务使用 `TAG-*` 名称，报告包含 `assert.tag_takeover` 和 `assert.tag_wait_release` 事件。原有任务统计仍描述 `LONG-*` 批量任务及初始化探针。接管断言检查被中断任务的 ID、owner 和 lease version，初始化重试不能满足条件。审计要求全局/分组峰值确实达到 3/2，且零超限、permit/计数一致、终态映射无残留。

断联测试只关闭某个 Worker 的已有数据库连接，并拒绝新连接，执行器的阻塞信号服务仍可访问。该场景调长原 Worker 的心跳间隔，单独验证续租失败导致的租约过期；进程必须保持存活，并在网络恢复后重新连接。提交旧 owner 的结果不得释放新尝试的名额。心跳失败引发停机的路径由已有测试验证。

## 常规运行

```bash
go run ./cmd/anclax install  # 首次检出或修改生成工具版本后执行
make test
make chaos-smoke  # 确定性场景 + 10 轮随机故障
ANCLAX_TASKCORE_CHAOS_SEED=8675309 make chaos  # 200 轮随机故障
```

`make test` 顺序执行 unit/race、确定性 runtime、数据库 smoke/stress 和短容器 chaos。数据库和容器测试要求已安装并启动 Docker；无论通过 Make 入口还是直接运行 smoke，Docker 不可用都会报错。`make ut` 不需要 Docker。

本地运行时，通过 `ANCLAX_SMOKE_POSTGRES_IMAGE` 和 `ANCLAX_TASKCORE_CHAOS_POSTGRES_IMAGE` 选择数据库镜像。可以分别使用 PostgreSQL 15 和 17，并将 `ANCLAX_TASKCORE_CHAOS_SEED` 设置为 424242 或 8675309，验证可复现的故障序列。chaos harness 将日志、报告和失败诊断写入测试输出中显示的产物目录。

## 性能测试

```bash
ANCLAX_TEST_REPORT_DIR=/tmp/anclax-reports make taskcore-perf

ANCLAX_TAG_LOAD_SECONDS=10 \
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

上述阈值用于发现明显回归，不是生产 SLA；稳定的压测机器可以设置更严格的预算。设置 `ANCLAX_TEST_REPORT_DIR` 后，结果写入 `tag-load.json`。

大规模迁移压测作为本地一次性验证，结果记录在对应 PR 中。常规迁移 smoke 只使用上述固定规模的兼容性数据。

Mock 可以系统列举模块约定的分支、边界条件和依赖返回值，但无法穷尽无限的状态组合，也无法证明真实依赖符合 mock 假设。状态机和性质测试可以探索更多操作顺序；数据库集成测试和进程故障测试负责验证上述真实边界。
