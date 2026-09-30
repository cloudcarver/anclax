# 跨层组合模型

[Scheduler.tla](Scheduler.tla) 直接实例化已有 `TaskLease` 和 `TagSlots`，增加 Worker
批量预留、数据库回复缓冲、执行周期以及预取 scheduler 的所有权。运行：

```sh
python3 formal/check.py --model composed --timeout 240
```

## 一个动作如何跨越组件

`BeginPrepare` 取得任务和 scheduler 的逻辑行锁；中间执行槽位模型的 scan、guard、
fresh recheck 和私有写入。`FinishPrepare` **同时**提交全部 tag 占用和租约模型的
pending → ready。失败则 `AbortPrepare` 回滚私有分配，数据库任务状态不变。

Worker 先 `BeginBatch` 预留本地容量。`DBBatch` 将一组已锁定的 ready 任务原子改为
running，并将尝试身份放入回复缓冲；Worker 尚未收到回复，所以仍占用原预留。
`Receive` 将预留替换成执行周期。回复等待期间，数据库可以发生过期、控制操作和接管，
因此回复中的尝试可能已经过时；模型允许这个过程，不假设回复永远新鲜。

execute 返回进入 finalizing，继续占本地预算。通过身份检查的 finalize 持任务行锁，
提交时同时修改状态、释放本任务所有 tag、结束本地周期。回滚保留周期以待重试。
失败／失效的 finalize 也可以结束本地周期，遗留数据库租约由之后的清理处理。

配置屏障同时等待资源分配事务和 finalize 事务结束，再重建槽位。`Configure` 可以
反复增加或减少限额；该有限模型没有“最多一次配置变化”的限制。

## 联合检查的不变量

| 性质 | 内容 |
| --- | --- |
| `LeaseSafety`、`SlotSafety` | 两个原模型的全部安全不变量 |
| `Coupling` | 任务有资源包，当且仅当它是槽位分配器中的已准入持有者 |
| `ProtocolType`、`BatchLifecycle` | 阶段和身份合法；缓冲回复来自已签发尝试，尚未生成执行周期；预留与批次存在性一致 |
| `BudgetSafety`、`ResponseContract` | 每个 Worker 的周期加预留不超限；数据库回复总数与 strict 数不超过本次预留 |
| `KnownAttempts` | 本地执行／finalize 周期对应已经签发的租约尝试 |
| `SchedulerFence` | 旧 scheduler 身份不能开始新的资源分配事务 |
| `SerialSafety` | 同一 serial key 至多有一个 ready 或有效执行租约 |

serial 还在准入时检查静态队首：更小 task ID 的 pending／ready 任务阻止后续任务。
这是对实际 SQL 多字段排序键的简化；它没有覆盖任意动态重排、插入或属性修改。
预取用一个候选事务表示一次提交，真实一批中的多个候选被拆开，允许候选提交之间的
额外交错。手动 pending claim 没有纳入这个组合；相关路径另有租约模型和数据库测试。

## 配置范围

| 配置 | 任务 | Workers | tags × slots | task／scheduler 最大版本 | 本地容量 |
| --- | ---: | ---: | --- | ---: | ---: |
| `ComposedLease` | 1 | 1 | 1 × 1 | 2 | 1 |
| `ComposedTakeover` | 1 | 2 | 1 × 1 | 2 | 1 |
| `ComposedMultiTag` | 2 | 1 | 2 × 1 | 1 | 2 |
| `ComposedBatch` | 2 | 1 | 1 × 2 | 1 | 2 |
| `ComposedSerial` | 2，同一 serial key | 1 | 1 × 2 | 1 | 2 |

限额可在 `0..SlotCount` 内反复变更；所有任务需要模型里的全部 tag。当前组合配置只
使用普通任务。strict 组合数量不变量在模型中存在，但其非零行为由独立 Worker 模型、
参数化领取合同和真实 SQL 矩阵验证，不能声称组合配置已遍历 strict 流量。

故障配置分别放开批量总数、scheduler fence、serial 占用检查，必须违反目标性质。
`ReachOldExecution` 要求找到“旧执行仍存在，而数据库版本已经改变”的状态。这符合
租约失效不能强制终止任意 handler 的语义，不是互斥租约失效。

## 条件活性

`ComposedCompletion` 使用同一个组合状态和动作，但限制为固定、有限的正常工作量：
两个 serial 任务、一个 Worker、一个 tag 槽位、无控制变更、无失败、配置固定且容量为正。
它显式添加逐任务准入／选择公平性、每阶段推进公平性，并要求最终完成且释放资源。
因此它证明的是该条件下的有限模型活性；没有把公平性解释成系统自动满足的事实。

其他配置不检查活性或数据库无死锁。该模块也没有提供任意规模的组合归纳证明，
更没有把 Go／SQL 程序直接交给语义验证器；详见[实现对应论证](../IMPLEMENTATION.md)。
