# 任务租约协议的形式化模型

这是 Anclax 的第一版 TLA+ 模型，研究**租约接管、迟到 finalize、控制状态和资源释放**。
TLC 已完成两个有限配置的安全性质检查，首轮结果见 [RESULTS.md](RESULTS.md)，当前结果
见[完整报告](../RESULTS.md)。后续增加了 `LeaseProofs` 的参数化局部协议证明和跨层组合
检查；它们的抽象范围不同，并非整个 Anclax 或本模型全部行为的无界正确性证明。

## 运行

在仓库根目录执行：

```sh
python3 formal/check.py --model tasklease
```

需要 Python 3.8+ 和 Java；当前验证环境使用 Java 17。第一次运行会从
[TLA+ 官方发布页](https://github.com/tlaplus/tlaplus/releases/tag/v1.7.4) 下载固定版本的
`tla2tools.jar`，校验 SHA-256 后缓存在 `.anclax/formal-tools/`。运行器不安装系统软件。
已有同一版本的工具时，可以指定：

```sh
TLA2TOOLS_JAR=/path/to/tla2tools.jar python3 formal/check.py --model tasklease
python3 formal/check.py --case TaskLease
python3 formal/check.py --case NoFinalizeFence
```

每次运行将输入副本、完整 TLC 输出、覆盖率和 `summary.json` 保存在独立的
`.anclax/formal/scheduler-*` 目录，终端会打印路径。报告记录模型、配置、对应实现文件和
工具的 SHA-256。源码散列用于识别审查版本，不会自动证明模型与代码一致。

该模型运行两个安全检查、三个故障变体和三个可达性检查；`make formal` 运行全部模型，
详见[总览](../README.md)。安全检查必须完整结束且搜索队列
为空；后两类必须产生指定不变量的反例和 TLC 的退出码 12。语法错误、类型错误、超时、
其他不变量错误均不能算通过。每个配置默认超时 120 秒，可用 `--timeout` 调整。

## 验证对象

状态定义在 [TaskLease.tla](TaskLease.tla)：

| 变量 | 含义 |
| --- | --- |
| `rows[t].status` | `pending / ready / running / paused / cancelled / completed / failed` |
| `rows[t].owner` | 当前数据库记录的 Worker，或 `None` |
| `rows[t].version` | `lease_version`，也用于 pause/resume 后的 fencing |
| `rows[t].lease` | 无执行租约、尚未过期、已过期 |
| `permits[t]` | 一组资源预留所属的版本；0 表示没有资源预留 |
| `issued` | 已经发给执行器的尝试身份 `(task, worker, version)`，包括旧尝试 |
| `finalizing[t]` | 已通过条件 UPDATE、持有任务行锁、尚未提交或回滚的事务 |
| 两个布尔监视变量 | 记录是否曾提交越权 finalize 或覆盖已提交的暂停／取消 |

`issued` 不因超时、崩溃或完成而删除。因此模型允许旧尝试任意迟到或重复返回，也允许
同一 Worker ID 再次领取任务。执行器停机、失联和提交确认丢失没有单独的网络状态：
它们在这个层次体现为任意延迟、不再发送请求、过期，以及已提交后再次发送旧请求。
网络重连和 Runtime 的重试策略没有被单独验证。

每个任务用一个抽象资源包代表其已申请的受限 tag 资源。这个模型**假设资源申请已经成功且
原子提交**，不模拟共享槽位竞争，也不验证 tag 容量、多 tag 分配、串行队列或动态配额。
两个任务的配置检查的是任务间的释放隔离，不是配额上限。

## 检查的安全性质

| 不变量 | 检查内容 |
| --- | --- |
| `TypeOK` | 所有状态变量保持在声明的有限域内 |
| `NoStaleFinalize` | 提交 finalize 时，请求中的 Worker 和版本仍匹配任务记录 |
| `ControlWins` | finalize 不改变已经提交的 `paused / cancelled` 状态 |
| `ResourcesAccounted` | 持有执行租约或处于 ready 时保留资源；释放后不留下无所有者的资源 |
| `LeaseShape` | ready 没有 Worker 执行租约；running 有所有者；最终完成／失败已释放租约 |
| `IssuedVersions` | 旧身份不会超前于当前版本；未清理的执行资源对应一个已发出的尝试 |

`ResourcesAccounted` 是状态一致性，**不代表资源一定最终释放**。暂停或取消的任务可以
继续持有旧租约和资源，直到 finalize 或恢复过程清理它们。

安全性质独立于被测开关定义。例如 `NoStaleFinalize` 检查提交时的身份匹配；
`FenceFinalize` 只控制 UPDATE 的版本检查。关闭这个检查会真正产生越权写入，
不会同时削弱被检查的性质。

## 与实现的对应关系

审查基准：`c37a6e647bec541b0cf82ce57205f17169c0ebe8`。下面是人工核对的映射，
不是经机器检查的 Go／SQL 到 TLA+ 的 refinement 证明。

| 模型动作 | 实现位置与抽象 |
| --- | --- |
| `Prepare` | [migration 16](../../sql/migrations/0016_ready_task_prefetch.up.sql) 的 `prefetch_task_supply`：准备资源，版本加一，提交 ready |
| `ClaimReady` | [task_batch.sql](../../sql/queries/task_batch.sql) 的 `ClaimTaskBatch`，以及 [tasks.sql](../../sql/queries/tasks.sql) 的 `ClaimTaskByID` ready 分支：接管资源，版本保持不变 |
| `ClaimPending` | [tasks.sql](../../sql/queries/tasks.sql) 的 pending claim 路径：设置 Worker／租约，版本加一；手动领取后状态仍可为 pending |
| `Control`、`Resume` | [tasks.sql](../../sql/queries/tasks.sql) 的 `UpdateTaskStatus`，以及 migration 16 的 ready 失效触发器 |
| `EditReady` | migration 16 的 `invalidate_task_reservation`：调度属性修改撤销 ready 资源并提升版本 |
| `Expire`、`Renew` | `lease_expires_at` 与数据库时间的比较，以及 `RefreshTaskLock(s)`；把具体时间抽象为有效／过期 |
| `Recover` | [migration 15](../../sql/migrations/0015_task_admission.up.sql) 的 `maintain_task_concurrency`；migration 16 将清理后的 running 规范化为 pending |
| `Release` | `ReleaseTaskLockByWorker` 与 `release_task_tags_on_unlock` |
| `BeginFinalize` | `FinalizeTaskAttempt` 的任务行锁、Worker／版本／状态条件 |
| `CommitFinalize` | 同一事务中提交状态更新和 `release_task_tag_permits`；`PreserveControl` 对应 SQL 的 `CASE WHEN status IN ('paused', 'cancelled')` |
| `RollbackFinalize` | [finalize_retry.go](../../pkg/taskcore/worker/finalize_retry.go) 的事务失败路径：状态及触发器写入一起回滚，允许之后再次尝试 |

模型不决定 handler 的结果，而从 pending、completed、failed、paused、cancelled 中任选
一个结果。它抽象了 [lifecycle_policy.go](../../pkg/taskcore/worker/lifecycle_policy.go) 的
多种输出，没有证明 retry 次数、cron 时间或事件写入的正确性。

三个容易建模错误的实现细节在这里被保留：

1. **Resume 提升版本，但不一定释放旧租约。** 因而 `permits[t]` 可以小于
   `rows[t].version`。将两者始终相等当作不变量会误报。
2. **SQL finalize 没有租约截止时间条件。** 只要尚未被接管或撤销，过期但仍匹配的请求
   在数据库层仍可能成功。Runtime 的取消／deadline 可以进一步限制它，模型没有依赖
   这些额外限制。
3. **Ready 没有 TTL。** `Expire` 只作用于 Worker 执行租约，不会让 ready 自动释放。

## 事务与时间的抽象边界

针对同一个任务，claim、控制操作、恢复和释放均表示持有任务行锁后提交的一步。
finalize 显式拆成取得锁、提交、回滚；持锁期间其他任务操作可以推进，但同一任务的
数据库写入不能推进。时间可以在持锁期间流逝，变成 expired 不代表旧事务失去行锁。
事务内尚未提交的行及触发器修改在这里不对其他事务可见。

这依赖 PostgreSQL 的行锁和事务原子性，参见
[官方锁文档](https://www.postgresql.org/docs/current/explicit-locking.html)。它没有假设
整个数据库的 READ COMMITTED 事务是可串行化的。尤其没有把共享 tag 分配器的多语句
检查折叠成一个“已经证明正确”的操作；分配器正确性是本模型明确未验证的前提。

用 valid／expired 代替时间可以探索过期前后发生的所有所列操作，但不表达两个任务
截止时间的具体先后，也不验证 `statement_timestamp()`、等待锁期间的快照、续租批次、
本地 deadline 和时钟偏差。`Renew` 在这个抽象中是停顿步骤，因此不能据此声称续租时序
已被证明。资源用单个版本代替槽位集合，也不验证槽位数量或底层 advisory lock。

## 有限范围与活性

| 配置 | 任务 | Worker | 最大版本 |
| --- | --- | --- | --- |
| `TaskLease.cfg` | 1 | 2 | 3 |
| `TwoTasks.cfg` | 2 | 2 | 2 |

达到版本上限后，需要增加版本的动作不再可用；没有取模、重用或回绕版本。控制操作也可能
增加版本，因此“最大版本”不等于“最大尝试次数”。检查覆盖各配置的全部可达状态，
不是固定长度的随机轨迹，也不意味着覆盖任意大的参数。

`Spec` 没有公平性条件，配置关闭 deadlock 检查，因为终态和版本上限都可能让模型停止
推进。**这次不检查活性或无死锁**，不声称任务最终成功、一定被领取或资源最终释放。

还未覆盖：完整 Engine／Runtime 并发预算、进程注册、预取调度器 singleton fencing、
serial 顺序、动态 tag 配额和 backfill、任意直接 SQL 写入、删除、system 任务，以及
外部业务副作用的 exactly-once。允许通过底层查询直接把仍持租约的任务标记 completed／
failed 的管理路径也不在这组生命周期动作中。

## 模型自检与后续证明

三个故障变体分别删除 finalize 版本检查、控制状态保护、资源释放的任务范围。它们必须
产生反例，才能确认相应性质能识别这些错误。三个 witness 配置使用刻意为假的不变量，
要求找到同 Worker 接管、ready 接管、resume 保留旧租约的路径，避免模型误把场景禁止。
Witness 的反例是预期行为，不是当前实现缺陷。

参数化局部协议的归纳思路是：初始状态没有所有者和资源；claim 成对建立所有权与资源；
resume 仅提升 fence；finalize 的版本检查与任务行锁使身份在提交前保持匹配；提交时的
CASE 保留控制状态，解锁触发器只清理该任务资源。该协议现已在
[LeaseProofs.tla](../proofs/LeaseProofs.tla) 中通过 TLAPS 检查，不限制版本数量，并提供
逐任务局部更新的提升引理。它是单任务状态投影，没有形式化推导本模型全部历史尝试
集合、实际 SQL 或共享配额的行为。

[组合模型](../composed/README.md) 直接实例化本模型与[共享槽位模型](../tagslots/README.md)，
同时检查资源耦合、Worker 批量预算、serial 与 scheduler 身份。其完整交错仍是有限检查。
其他参数化结论见[证明说明](../proofs/README.md)，源码与协议之间的对应和剩余可信边界见
[实现对应论证](../IMPLEMENTATION.md)。
