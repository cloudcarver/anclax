# 第二轮验证结果：共享槽位和 Worker 容量

2026-09-29，审查的实现基准为 `c37a6e647bec541b0cf82ce57205f17169c0ebe8`。
本轮增加共享 tag 槽位模型、Worker 预算模型和参数化容量证明，并复跑第一轮租约模型。
没有修改生产 Go／SQL。第一轮租约反例和数据库测试另见
[第一轮报告](tasklease/RESULTS.md)。

**结论：所列有限配置中的安全检查全部通过；抽象容量协议的 16 个 TLAPS 证明义务全部
通过。** 13 个 TLC 故障变体产生预期反例，9 个可达性检查找到目标状态；另一个 TLAPS
故障变体在归纳步骤被拒绝。模型组合、实现 refinement 和活性尚未证明，完整范围见
[总览](README.md)。

## TLC 有限检查

执行 `make formal`，TLA+ tools 1.7.4（TLC 2.19），Java 17，单线程广度优先搜索，
`-fp 0 -seed 1`。没有状态约束剪枝或对称性约简，五个正向配置均完整结束、队列为空。

| 正向配置 | 有限范围 | 生成状态数 | 不同状态数 |
| --- | --- | ---: | ---: |
| `TaskLease` | 1 task、2 Workers、version 0–3 | 4,973 | 1,493 |
| `TwoTasks` | 2 tasks、2 Workers、version 0–2 | 1,374,101 | 207,025 |
| `MultiTag` | 2 tasks、2 tags、各 1 slot，不同 tag 需求 | 283 | 81 |
| `Overflow` | 3 tasks、1 tag、3 slots，至多一次缩容 | 34,069 | 6,585 |
| `WorkerBudget` | 业务容量 2、控制容量 1、批量 2、最多 3 个 ID | 159,244 | 21,324 |

五个配置的不同状态数相加为 **236,508**，不是组合系统的状态空间大小。
TLC 使用状态指纹；结果仍依赖模型和检查工具，不是独立内核复核的证明证书。

本地完整记录：`.anclax/formal/scheduler-rn33d1xm/summary.json`。每个配置的目录中
都有输入副本和 `tlc.log`；报告记录工具、模型、配置和被映射实现文件的 SHA-256。

```text
TaskLease.tla:
56fd2a0e2c82ae7a8af810163d64123d2515b9928b0f04d6cecf7f88568dd478
TagSlots.tla:
69d7ff10d8ba6264b53d4c9d2d35903c167765d34bb47bfa17b1a72d73315886
WorkerBudget.tla:
64cf2fbea67254d8990d06775376b30ea2d772ac30202a286ba99decfb7cb71e
```

## 故障变体与反例

这些故障开关只存在于模型，未改动实际实现。第一轮三个租约故障变体继续通过。
本轮新增十个变体，均以 TLC 退出码 12 违反指定不变量：

| 故障变体 | 删除或破坏的保护 | 违反的性质 |
| --- | --- | --- |
| `StaleSlot` | 取得槽位 guard 后重新检查所有权 | `AllOrNothing` |
| `PartialRollback` | 候选失败时回滚部分分配 | `AllOrNothing` |
| `NoOverflowCount` | retired 占用存在时的用量检查 | `NoNewOversubscription` |
| `NoOverflowGuard` | fresh count 到 commit 之间的 overflow guard | `NoNewOversubscription` |
| `NoConfigBarrier` | 配额变更前等待任务事务结束 | `NoNewOversubscription` |
| `NoBatchReservation` | 发起批量领取前预留容量 | `BudgetAccounting` |
| `EarlyCapacityRelease` | finalize 期间继续占用容量 | `BudgetAccounting` |
| `DuplicateFinalize` | 排除已经结束的周期 | `BudgetAccounting` |
| `AdmissionAfterStop` | 停机后的准入守卫 | `NoPostStopAdmission` |
| `LostFallbackReservation` | strict fallback 期间保留预留 | `BudgetAccounting` |

### 去掉 overflow guard

实际反例共 38 个状态，可以压缩为：

1. 三个任务占满三个槽位，限额从 3 降到 2，第三个槽位成为 retired。
2. 前两个任务释放资源；仅剩 retired 槽位上的任务，用量为 1。
3. 两个新的分配事务都在提交前读到 `1 < 2`，并分别选中两个空的普通槽位。
4. 两个事务先后提交，用量变成 3，违反新限额 2。

这里各事务选的是不同槽位，单独的槽位锁无法保护“count 后新增”的总量条件。
实际实现保留 overflow guard，通过后的事务持锁到提交或回滚。

### 去掉配置屏障或槽位重新检查

`NoConfigBarrier` 的轨迹是在一个任务已写入私有槽位、尚未提交时，将限额降到 0，
随后原任务提交，违反新限额。实际配置语句的 tasks 表锁阻止这种交错。

`StaleSlot` 的轨迹是两个事务先后观察同一个空槽位。第一个事务提交后，第二个取得
已经释放的 guard；缺少新的条件 UPDATE 检查时，它覆盖第一个任务的所有权，导致
第一个已准入任务没有资源。实际实现的新语句重新检查阻止该覆盖。

### 过早或重复归还 Worker 容量

`EarlyCapacityRelease` 在 execute 返回时将业务计数降为 0，但周期仍在 finalizing，
立即破坏“计数 = 活跃周期 + 批量预留”的等式。

`DuplicateFinalize` 的最短反例为领取失败先结束周期，随后对同一已结束周期处理一个
finalize 结果，计数被减到 -1。实际实现按周期存在性和 phase 过滤此类结果。
该模型变体允许已结束周期重新进入完成处理，不能解释成“删除某一行 Go 检查就必然
复现同一反例”。

## 可达性检查

租约模型的三个 witness 继续通过。新模型的六个 witness 分别找到：缩容后的存量
超额、事务部分分配、过期游标、strict 存量超过新 cap、业务满载时的控制任务、
停机状态下仍有待 finalize 工作。它们检查刻意为假的不变量，预期反例说明模型没有
排除这些场景；并不表示当前实现存在故障，也不提供最终完成保证。

## TLAPS 参数化证明

执行 `make formal-proof`，使用 TLAPS 1.5.0 的 SMT／PTL 后端和 `--threads 1 --nofp`。
六个定理共 **16 个证明义务**通过，覆盖初始状态、动作保持、时序归纳、batch／strict
预留公式及一个带前提的 overflow 算术引理，详见[证明说明](proofs/README.md)。

该容量结论适用于任意正自然数业务容量、任意自然数控制容量及任意执行长度。
它没有证明整个 Worker、租约或共享槽位实现，只证明所列抽象协议。

故障变体 `MissingReservationBound` 将预留条件从 `active+n <= Capacity` 放宽为
`active <= Capacity`。TLAPS 在 `InductiveInvariant` 上报告 1/16 义务失败，退出码 3；
这符合预期。一个直接反例是容量 1、当前空闲却允许预留 2。

本地完整记录：`.anclax/formal/proofs-oft78k9m/summary.json`，包含正向证明、故障变体、
日志、实际命令和工具散列。

```text
SchedulerProofs.tla:
1066d5782a36f23528d7af269249f1f134a5257cd9ca28c34485bf2ba93477c5
TLAPS 1.5.0 Linux x86_64 installer:
ebb7a3f271bdb564f74cb0a2767ef7b9ff7045621a9be7c50d363a03c2e6f08a
```

## 实现对照测试

以下现有测试本轮实际运行并全部通过：

```sh
go test -race ./pkg/taskcore/worker ./pkg/taskcore/dtmtest -count=1 -timeout=120s

go test -tags=smoke ./pkg/taskcore/e2e \
  -run '^(TestTaskSlotAdmissionSmoke|TestTaskAdmissionSmoke|TestTaskTagConcurrencySmoke)/(finalizers_do_not_acquire_allocation_guards|uncommitted_allocator_does_not_block_another_slot_or_its_release|lowered_limit_allows_progress_as_soon_as_usage_falls_below_limit|rejected_reclaim_preserves_the_previous_attempt_allocation|admission_and_quota_changes_reject_stale_transaction_snapshots|quota_activation_waits_for_inflight_claim_before_backfill|rejected_candidate_releases_partial_guards|uncommitted_release_does_not_block_another_slot_or_require_wakeup|batch_honors_strict_cap_labels_tag_limits_and_serial_heads|concurrent_batches_and_finalizers_drain_without_deadlocks|all_tags_or_none_and_no_retry_consumption|multi_worker_two_tag_contention)$' \
  -count=1 -v -timeout=180s
```

PostgreSQL smoke 共选中 12 个场景，包括分配与释放交错、部分回滚、缩容、隔离级别
拒绝、配置屏障、批量 strict 限制和多 Worker 竞争。测试也覆盖了模型未纳入的部分
实现行为，例如旧尝试 reclaim 和 serial 队首；测试通过不能将它们升级为已证明性质。
本轮未运行完整 `make test` 或长时间 chaos，也未自动回放所有 TLC 轨迹。
