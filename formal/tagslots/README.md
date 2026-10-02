# 共享 tag 槽位协议

[TagSlots.tla](TagSlots.tla) 检查 migration 15 中的独立槽位分配协议。
模型保留 READ COMMITTED 下语句之间的交错：读到空槽位、取得 advisory guard、执行
新的条件 UPDATE、提交事务分别是独立动作。运行：

```sh
python3 formal/check.py --model tagslots
```

## 状态与实现映射

审查基准为 `c37a6e647bec541b0cf82ce57205f17169c0ebe8`，主要代码是
[0015_task_admission.up.sql](../../sql/migrations/0015_task_admission.up.sql)。

| 模型 | 实现及含义 |
| --- | --- |
| `owners`、`pending` | 已提交的槽位所有权、事务私有的槽位写入，后者提交前不对其他事务可见 |
| `Begin`、`active` | 一个任务同时只有一个分配事务；代表任务行锁的排他效果 |
| `InspectOverflow`、`TakeOverflowGuard`、`CountOverflow` | `try_admit_task_tags` 的 retired 槽位检查、tag overflow advisory guard、取得 guard 后重新计数 |
| `Scan` | `try_task_tag_slot` 的游标快照；该结果可能在取得 guard 前过期 |
| `TakeSlotGuard`、`WriteSlot` | 每个槽位的 try advisory lock，以及新语句中的 `task_id IS NULL AND NOT retired` 条件 UPDATE |
| `Abort` | 候选任务的异常块／savepoint 回滚全部部分分配和锁 |
| `Commit` | 所需 tag 全部取得后，原子发布槽位所有权和任务准入 |
| `Release` | `release_task_tag_permits` 只清理本任务；不取得分配或 overflow advisory guard |
| `Resize` | 配置语句先取得 tasks 表 EXCLUSIVE 锁，等待任务事务结束；再按已占用槽位优先重建，超过新上限的槽位成为 retired |

`position` 按 tag 顺序推进。`guards` 和 `overflowGuard` 保持到提交或回滚，其他任务可以
在此期间读已提交状态。无法取得 guard、配额已满、条件 UPDATE 失败都可以触发 Abort。
实际代码尝试其他槽位的路径在模型中抽象成回滚后重新 Begin；这个变化适合检查所列
安全性质，不用于论证进度或公平性。

## 检查的性质

| 不变量 | 含义 |
| --- | --- |
| `TypeOK` | 状态、所有者和槽位在声明域内 |
| `AllOrNothing` | 已准入任务对每个所需受限 tag 恰好占一个槽位；其他任务无已提交占用 |
| `PrivateWritesGuarded` | 私有写入拥有相应槽位 guard；事务结束后没有私有写入或残留 guard |
| `NoNewOversubscription` | 新事务提交前，每个所需 tag 的已提交用量必须小于当时上限 |

最后一个性质用独立审计变量记录每次提交是否违反限额；不会随故障开关一起放宽。
缩容本身可以造成 `Usage > limit`，所以它不是被禁止的状态。
`ReachOverflow` 明确要求找到这样的状态，防止误把合理的缩容语义排除在模型外。

overflow guard 保护“计数后再分配”的竞争，释放不需要该 guard。如果最后一个 retired
占用被释放，后来的分配可能直接走普通槽位路径；模型也允许这种交错。因此不能简单
把“取得 overflow guard 后用量只会减少”当作整个协议的永久不变量。

## 有限配置与模型自检

| 正向配置 | 任务 | tag | 每个 tag 的初始槽位 | 缩容 |
| --- | ---: | ---: | ---: | --- |
| `MultiTag` | 2 | 2 | 1 | 无；task 1 需要两个 tag，task 2 只需要第二个 |
| `Overflow` | 3 | 1 | 3 | 任意时刻至多一次，降到 0、1 或 2 |

这些配置可以反复领取和释放，不限制执行步数；任务数、槽位数和配置变更次数仍是有限的。
它们没有覆盖多 tag 与缩容同时发生的全部组合。没有状态约束剪枝或对称性约简。

五个故障变体分别去掉槽位重新检查、部分分配回滚、overflow 计数、overflow guard、
配置变更屏障，均必须产生指定反例。三个 witness 要求到达缩容后超额、事务部分分配、
游标读到的空槽位已被别人占用的状态。完整结果见 [验证记录](../RESULTS.md)。

## 前提和未覆盖部分

模型以 PostgreSQL 事务原子性、任务行锁排他、每条新语句读取已提交状态为前提。
实际协议主动拒绝非 READ COMMITTED 隔离级别，配置屏障也必须在该隔离级别使用。
数据库语义参见 [事务隔离](https://www.postgresql.org/docs/current/transaction-iso.html)
和 [显式锁](https://www.postgresql.org/docs/current/explicit-locking.html)。本模型没有
形式化 PostgreSQL MVCC、索引唯一约束、锁管理器或死锁检测器本身。

`Release` 的版本授权由租约协议提供；此独立模型没有任务版本。
它从没有占用的状态开始，只模拟成功准入、释放和一次缩容，没有无限额 tag 的切换、
任意回填、连续修改、已有旧尝试资源的 reclaim、任务删除或任意直接 SQL 写入。
重建时按 task ID 排列存量；存量次序对这里的资源计数性质没有业务含义。

没有公平性假设，deadlock 检查关闭，不验证“等待者最终成功”。READ COMMITTED 的实际
行为还用现有 PostgreSQL smoke 测试对照；这不是经机器检查的 SQL refinement 证明。

## 组合检查与参数化补充

[组合模型](../composed/README.md) 将本分配器与任务租约直接组合，检查原子发布、版本
授权释放及资源耦合，并允许限额反复变化。[QuotaProofs.tla](../proofs/QuotaProofs.tla)
证明单 tag 聚合协议在任意数量下的准入边界，包含多次配置、解除限额和重新回填；另有
原子多 tag 提交的计数引理。该聚合协议假设正确的互斥预留，没有机器检查本多语句模型
到聚合协议的无界 refinement。两种证据及真实数据库检查分别见[完整报告](../RESULTS.md)
和[实现对应论证](../IMPLEMENTATION.md)。
