# 调度系统验证结果

2026-09-30。审查的实现基准为 `c37a6e647bec541b0cf82ce57205f17169c0ebe8`；
21 个生产源码文件的具体散列在 [implementation.json](implementation.json)。
本轮新增模型、证明、直接实现检查与统一运行器，没有修改生产 Go／SQL。
[第一轮](tasklease/RESULTS.md)和[第二轮](RESULTS-phase2.md)的记录保留供追溯。

**`make verify-scheduler` 完整通过。** 六组抽象协议的 130 个 TLAPS 证明义务通过；
43 项 TLC 检查均取得预期结果；真实 Engine 有界遍历、相关包的 race 测试，以及
14 组 PostgreSQL smoke 全部通过。以下分别列出证据与适用边界。

这仍不是原始 Go／SQL 的端到端机器检查证明：源码对应目前是人工论证和执行检查，
完整组合协议的全部交错也没有任意规模的归纳证明。不能将局部参数化证明、有限模型
检查和实现测试简单相加，称作整个系统无条件正确。

## 统一执行记录

```sh
make verify-scheduler
```

| 阶段 | 结果 | 本次用时 |
| --- | --- | ---: |
| 实现映射 | 21 个文件、6 组映射与审查版本一致 | 0.03 秒 |
| TLC | 43 项均得到指定结果 | 217.95 秒 |
| TLAPS | 130 个正向义务通过，3 个故障变体在指定归纳步骤失败 | 23.57 秒 |
| Go | 4 个相关包的 race 测试及实际 Engine 有界遍历通过 | 51.25 秒 |
| PostgreSQL | 14 组、106 个叶子测试通过，包含新增 28 组领取合同矩阵 | 74.54 秒 |

完整日志和机器可读报告保存在本地被忽略的产物目录：

- 总报告：`.anclax/formal/verification-2i4ue4ms/summary.json`
- TLC：`.anclax/formal/scheduler-1qkz5rlz/summary.json`
- TLAPS：`.anclax/formal/proofs-z2knny6l/summary.json`

总报告记录每阶段实际命令、退出码、耗时、日志、子报告路径与通过的测试名，并检查
指定测试确实运行。工具环境为 Java 17、TLA+ tools 1.7.4（TLC 2.19）、TLAPS 1.5.0、
Go 1.27.1 linux/amd64；数据库使用现有 Docker PostgreSQL 15 smoke harness。
工具固定版本、下载校验和安装方式见[总览](README.md)。

## 参数化证明

| 模块 | 通过的证明义务 | 结论范围 |
| --- | ---: | --- |
| `SchedulerProofs` | 16 | 任意容量下的 Worker 预算、批量与 strict 预留公式 |
| `LeaseProofs` | 20 | 无版本上限的局部租约协议：fence、控制状态、资源归属、释放与续租条件 |
| `AdmissionProofs` | 20 | 候选／LIMIT 数量合同、预算组合、strict 饱和、回填及续租时间引理 |
| `QuotaProofs` | 10 | 聚合槽位协议支持反复配置、解除限额、回填和原子多 tag 提交计数 |
| `ProgressProofs` | 52 | 任意自然数工作量在公平下降时最终归零，以及停机工作量的下降公式 |
| `PolicyProofs` | 12 | retry／cron／控制／丢失租约等纯决策合同 |

合计 **130**，全部实际重新证明，使用 `--threads 1 --nofp`，无省略证明。
三个故障变体分别移除容量界限、finalize epoch 条件和 overflow 序列化要求，均以
退出码 3 在 `Inv /\ [Next]_vars => Inv'` 上报告一个失败义务。失败只用来确认证明
能够拒绝这种错误，不能把证明失败本身当成执行反例。

参数化表示这些数学协议不受本次 TLC 的任务数／版本数限制；不表示原始 Go 整数不会
溢出，也不表示局部协议已经自动组合为无界系统证明。精确前提见[证明说明](proofs/README.md)。

## TLC 有限安全性与活性

单线程广度优先搜索，固定 `-fp 0 -seed 1`。正向配置均完整结束、队列为空，无状态
约束剪枝或对称性约简。安全配置没有附加公平性，也不检查活性；两项活性配置显式声明
各自的公平性和环境限制。

| 正向配置 | 有限范围／作用 | 不同状态数 |
| --- | --- | ---: |
| `TaskLease` | 1 task、2 Workers、version 0–3 | 1,493 |
| `TwoTasks` | 2 tasks、2 Workers、version 0–2 | 207,025 |
| `MultiTag` | 2 tasks、2 tags、各 1 slot，不同 tag 需求 | 81 |
| `Overflow` | 3 tasks、1 tag、3 slots，至多一次缩容 | 6,585 |
| `WorkerBudget` | 业务容量 2、控制容量 1、batch 2、ID 至多 3 | 21,324 |
| `ComposedLease` | 1 task、1 Worker、1 slot、version 至多 2 | 14,286 |
| `ComposedTakeover` | 1 task、2 Workers，跨 Worker 接管 | 1,088,892 |
| `ComposedMultiTag` | 2 tasks、1 Worker、2 tags，各 1 slot | 284,796 |
| `ComposedBatch` | 2 tasks、1 Worker、1 tag、2 slots、batch 2 | 533,680 |
| `ComposedSerial` | 同上，两个任务共享 serial key | 320,952 |
| `ComposedCompletion` | 固定正常工作量与公平性下，组合协议完成并释放资源 | 102 |
| `FairCompletion` | 2 tasks、容量 1、允许崩溃，显式公平性下最终完成 | 64 |

各配置的不同状态数相加为 **2,479,280**，不是一个组合系统的全局去重状态总数。
组合配置允许反复配额变更，但只实例化普通任务；其余参数、简化和遗漏路径见
[组合模型说明](composed/README.md)。TLC 状态指纹和检查工具属于可信基础。

43 项的构成是：10 项安全检查、2 项条件活性检查、16 个故障变体、10 个可达性 witness、
4 个活性反例、1 个外部副作用反例。故障变体／witness 必须以退出码 12 违反指定不变量；
活性反例必须以退出码 13 违反配置中唯一指定的时序性质。语法错误、超时或其他工具
错误都不能被算作成功。

新增组合故障变体覆盖过量批量回复、旧 scheduler 身份继续准入、跳过 serial 检查。
已有租约、槽位和 Worker 的 13 个故障变体全部复跑。`ReachOldExecution` 确认允许
数据库已接管、旧 handler 仍在本地执行的状态；这是租约协议的行为边界。

## 真实实现检查

[Engine 有界遍历](../pkg/taskcore/worker/formal_conformance_test.go) 直接调用生产
`Engine.Apply`，从实际周期和 batch 独立计算预算，检查停止后的准入和工作量、旧回复
及重复完成。每组业务容量 2、control 容量 1；深度至多 7，保留的 cycle ID 至多 3。
超过 ID 保留范围的已生成转移仍检查性质，但不再入队，因此不能称作实现全状态穷举。

| batch | strict 配置百分比 | 访问状态 | 检查转移 |
| ---: | ---: | ---: | ---: |
| 0 | 0 | 17,632 | 167,691 |
| 0 | 50 | 28,099 | 271,863 |
| 0 | 100 | 32,089 | 313,262 |
| 2 | 0 | 8,547 | 92,965 |
| 2 | 50 | 14,081 | 157,837 |
| 2 | 100 | 14,545 | 164,144 |
| 合计 | | **114,993** | **1,167,762** |

执行的相关包为 `worker`、`dtmtest`、`store`、`ctrl`，均带 `-race -tags=formal`。
已有测试补充覆盖生命周期决策、续租、错误路径和 Runtime 停机等行为。

[新 SQL 矩阵](../pkg/taskcore/e2e/formal_contract_smoke_test.go) 调用真实生成查询，
28 组参数组合覆盖 batch 1–4、所有合法 strictSlots 和两种 label 筛选。断言实际返回
数量、唯一 ID、Worker／running 状态、lease version 保持和 tag 资源归属，而不只检查
“返回数量不超限”。每个参数组合在真实事务中执行并回滚恢复 fixture。

另外 13 组数据库回归覆盖生命周期、共享 tag 并发、准入与槽位锁、ready 预取／控制、
迁移、续租、过期租约和供给决策。14 个顶层测试组的通过事件连同子测试共 116 个，
去掉有子项的父节点后为 **106 个叶子测试**，避免将父子重复计数。
本次没有运行完整 `make test` 或长时间容器 chaos，也没有自动重放全部 TLC 轨迹。

## 正确性结论与明确反例

在所列模型和对应合同内，租约 fence、资源归属与原子分配、预算计数、动态配额的新增
准入约束、serial 排他和 scheduler 接管检查均通过。缩容后存量暂时超过新上限是允许
的语义，禁止的是违反当前规则的新增分配。任务最终完成和停机收敛需要额外活性前提。

以下承诺有可重复运行的反例，不能无条件给出：

- 没有公平性、永久零配额或 handler 永不返回时，所有任务仍最终完成。
- strict backlog 持续存在且 strictSlots=batchSize 时，普通任务不会饥饿。
- 任意外部副作用 exactly-once：副作用成功、完成状态未提交时崩溃，可导致重试重复副作用。

停机后仍允许已有领取周期继续 fallback；本地 drain 不要求持久 ready 队列归零。
失效租约也不能强制停止旧 handler，因此不能将资源协议上限解释成所有残留外部执行
的物理数量上限。反例、活性前提和业务含义见[活性说明](progress/README.md)。

原始源码 refinement、任意规模的完整组合归纳、真实时钟／网络／数据库锁管理器、
任意路由和 serial 重排、迁移每个中间状态、event／hook 的完整副作用一致性仍未获得
端到端演绎证明。[实现对应论证](IMPLEMENTATION.md) 分开说明哪些是已证明合同、
哪些由实际测试支持、哪些仍为外部假设。
