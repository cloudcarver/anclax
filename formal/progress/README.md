# 活性条件和不成立的承诺

安全性说明某类错误不会出现，活性说明某件事最终会发生。故障、永久暂停、零配额、
没有匹配 Worker 或 handler 永不返回，都可能让安全系统无限等待。

## 已检查的条件活性

1. [组合模型](../composed/README.md) 的 `ComposedCompletion`，在固定有限工作量、正
   容量、稳定可用性、无失败与控制变更、逐任务公平性下，最终全部完成并释放 tag 资源。
2. [Progress.tla](Progress.tla) 允许任务反复崩溃返回 pending，但假设逐任务的准入、
   领取、执行具有强公平性，finalize 具有弱公平性。`FairCompletion` 检查两个任务共享
   一个容量时最终完成。崩溃恢复在此模型中为一步，它没有重演真实数据库恢复过程。
3. [ProgressProofs.tla](../proofs/ProgressProofs.tla) 对任何自然数工作量证明条件停机
   drain，不限制初始任务数量或执行长度。公平性要求剩余工作最终有一步严格下降。

强公平性表示某个动作反复获得机会时最终会被选择；只要求“调度循环一直运行”不足以
保证每一个任务获得选择。该条件不能直接用于证明 strict 满载下的普通任务无饥饿。

本地 drain 的工作量为 `3*claiming + 2*executing + finalizing`，claiming 包括已发出
的批量预留，control 也计入。停止后已有领取周期可能继续 fallback／探测剩余 group，
不创建新周期；这类步骤可以保持工作量不变。批量回复、执行返回与 finalization 完成
会减少工作量。直接执行的 Engine 状态遍历也检查了停机后该量不会增加。

本地工作量归零不等于数据库所有 ready 任务都被清空：ready 是持久供给，没有 TTL。
过期资源清理还依赖时间推进和维护 Worker 可用，未声称在整个集群永久停机时资源自动释放。

## 应当失败的性质

```sh
python3 formal/check.py --model progress
python3 formal/check.py --model guarantees
```

| 检查 | 反例含义 |
| --- | --- |
| `NoFairness` | 没有公平性，系统可以永远停顿而不完成任务 |
| `StuckHandler` | handler／finalizer 永不返回时，公平调度也不能保证完成 |
| `ZeroQuota` | 容量永远为零时，准入动作从未可用，强公平性也无法创造容量 |
| `PriorityStarvation` | strict backlog 持续存在且 strictSlots=batchSize，每轮都可只返回 strict；普通任务无限等待 |
| `DuplicateSideEffect` | 外部副作用完成后、完成状态提交前崩溃，重试再次产生副作用 |

前四项要求 TLC 给出指定时序性质的反例（退出码 13）。副作用检查要求违反
`ExactlyOnce`（退出码 12）。这些是**当前合同无法无条件提供的保证**，并非要求修复的
模型故障变体。具体轨迹保存在各自 `tlc.log`。

strict 饱和还由 `AdmissionProofs!StrictSaturation` 对任意正批量证明：strict 可用数量
至少为 batch 且 strictSlots=batch 时，SQL 排序后的总 LIMIT 不会返回普通任务。
即使是公平的 poll 循环，也不能改变这一选择结果。

外部 exactly-once 不由数据库租约推导。需要副作用接收方去重、幂等操作或适当事务协议。
槽位限制约束资源占用和有效尝试，不能强制杀死忽略取消信号的旧 handler。

## 范围

没有假设业务任务一定成功、无限 retry 一定停止或 cron 任务最终进入终态。
日历、实时延迟、网络恢复、任意动态路由与整个 PostgreSQL 锁管理器未纳入活性证明。
这里列出的条件是定理前提，应结合部署和业务协议判断是否成立。
