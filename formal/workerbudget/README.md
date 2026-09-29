# Worker 容量与生命周期

[WorkerBudget.tla](WorkerBudget.tla) 检查 Engine 在批量领取、单任务领取、执行、
finalize、配置缩容和停机交错时的预算一致性。运行：

```sh
python3 formal/check.py --model workerbudget
```

## 状态与实现映射

审查基准为 `c37a6e647bec541b0cf82ce57205f17169c0ebe8`。Engine 的 `Apply` 由
[runtime.go](../../pkg/taskcore/worker/runtime.go) 的事件循环串行调用；每个模型动作
代表一次预算状态变更，远程操作的结果可以任意迟到。

| 模型动作 | 实现 |
| --- | --- |
| `BeginBatch` | [batch.go](../../pkg/taskcore/worker/batch.go) 的 `claimBatch`：查询发出前预留业务和 strict 容量 |
| `BatchResult` | `onClaimBatchResult`：扣除整个预留，加入实际返回的任务；停机和缩容后仍处理已发出请求的结果 |
| `BeginSingle` | [engine.go](../../pkg/taskcore/worker/engine.go) 的单条 poll、控制任务和已获准手动请求 |
| `Fallback` | strict 查询为空后转 normal 查询，直到结果返回仍保留 strict 预留 |
| `SingleSuccess` | 领取成功；strict 预留最终拿到普通任务时降为 normal 并归还 strict 计数 |
| `ExecuteResult` | 进入 `PhaseFinalizing`，继续占用容量 |
| `ClaimFailure`、`FinalizeResult` | `finishCycleResult` 只结束一个仍有效的周期并归还对应容量 |
| `SetCap` | 配置更新 strict 容量上限；存量不强制取消 |
| `Stop` | 停止新准入；已有周期仍允许完成 |

批量大小按 `min(batchSize, capacity-inFlight)` 预留；strict 大小按
`min(slots, max(0, strictCap-strictInFlight))` 预留。这两个公式还有独立的
[参数化证明](../proofs/README.md)。满批返回后立即再 poll 的实现路径，在模型中拆成
返回后可选择开始下一批，保留中间预算状态并允许更多事件交错。

## 检查的性质

| 不变量 | 含义 |
| --- | --- |
| `BudgetAccounting` | 业务计数 = 活跃业务周期 + 批量预留；strict 计数是其中的 strict 部分；控制计数等于活跃控制周期 |
| `CapacitySafe` | 业务不超过固定业务容量；strict 非负且不大于业务；控制不超过独立控制容量 |
| `NoPostStopAdmission` | Stop 后不创建新的领取周期或批次；允许兑现已有预留 |
| `BatchShape`、`TypeOK` | 批量是否存在与预留一致，所有状态值属于声明域 |

活跃周期包括 claim、execute 和 finalize。业务满载时，独立的控制容量仍允许准入。
strict 上限缩小后已有占用可以超过新上限，之前预留的批量结果也可以继续落地。
因此 `strictInFlight <= strictCap` 被用作刻意为假的 witness 性质，而不是安全断言。
已获准的领取周期在 Stop 后仍可继续 normal fallback 或探测剩余 group；这类后续查询
使用原周期的预留，不是新准入。

五个故障变体分别去掉批量预留、提前在 execute 完成时归还容量、允许重复 finalize
再次归还容量、允许 Stop 后准入、在 strict fallback 时提前归还 strict 预留。
它们必须破坏预算等式或停机性质。三个 witness 检查 strict 缩容存量、业务满载仍有
控制任务、停机状态下仍有待 finalize 工作。Witness 不证明这些工作最终完成。

## 有限范围和依赖合同

默认配置：业务容量 2、控制容量 1、批量上限 2、最多 3 个周期／批次 ID。
ID 单调分配且不复用，发出一个批次也消耗一个 ID；达到上限会停止新准入。
已有周期的执行和完成仍然可以发生。没有状态约束剪枝或对称性约简。

模型显式假设 Port 返回 `0 <= n <= 本次预留`，其中 strict 数量不超过本次 strict
预留，单任务领取也遵守 lane 合同。实际 `onClaimBatchResult` 信任 Port；该返回合同
必须由 [task_batch.sql](../../sql/queries/task_batch.sql) 和适配器保证。
`AdmissionProofs` 已参数化证明所对应的候选数量／LIMIT 公式，真实 PostgreSQL 矩阵
检查 28 组 batch／strict／labels 参数。SQL 到公式的对应仍是人工核对，没有经过 SQL
语义证明器；完整论证见[实现对应说明](../IMPLEMENTATION.md)。

周期 phase 守卫表达重复完成不会再次扣减容量。批量只接收当前请求的结果；实际实现的
`CycleID` 关联检查是模型前提，没有枚举任意错误 ID 或错误任务内容。模型也未验证
cycle ID 的整数溢出、标签权重、手动请求队列顺序、执行器副作用、续租和退出时序。
配置直接选择新 cap，省略配置版本排序，允许更多 cap 变化序列。

这是预算安全模型，不是 Runtime 全部并发行为的证明。没有公平性假设，deadlock 检查
关闭，无法从中推出任务一定获得 CPU、远程调用一定返回或停机一定结束。

## 实际 Engine 遍历与条件停机

[formal_conformance_test.go](../../pkg/taskcore/worker/formal_conformance_test.go) 在六组
配置下直接调用生产 `Engine.Apply`，以完整状态键遍历深度至多 7、保留 cycle ID 至多 3
的合法事件序列。检查预算、停止后的新准入、重复完成和错误 batch 身份，并验证停止后
`3*claiming + 2*executing + finalizing` 不增加。该测试包含于 `make verify-scheduler`。

[ProgressProofs](../proofs/ProgressProofs.tla) 证明公平下降的任意自然数工作量最终归零。
要应用于停机，还要求 handler、数据库调用与重试最终推进；这些环境条件不能仅由
预算安全推出。条件与反例见[活性说明](../progress/README.md)。
