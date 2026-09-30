# 第一轮验证结果

2026-09-29，审查的实现基准为 `c37a6e647bec541b0cf82ce57205f17169c0ebe8`。
本轮增加模型、运行器、Make 入口和文档，没有修改生产 Go／SQL。

**结论：在所列有限配置和抽象假设下，当前模型未发现安全性质反例。** 三个故障变体均
产生预期反例，三个可达性检查均找到目标路径。该结论不扩展为无限规模证明、完整实现
refinement 证明、活性保证或外部副作用 exactly-once。完整边界见 [README.md](README.md)。

## 可复现记录

执行 `make formal`，使用 TLA+ tools 1.7.4（TLC 2.19）、Java 17、单线程广度优先搜索、
`-fp 0 -seed 1`，没有状态约束剪枝或对称性约简。

```text
TaskLease.tla SHA-256:
56fd2a0e2c82ae7a8af810163d64123d2515b9928b0f04d6cecf7f88568dd478

tla2tools.jar SHA-256:
936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88
```

本地完整证据保存在 `.anclax/formal/tasklease-gv4t6vht/`，包括每个配置的输入副本、
`tlc.log` 和总报告 `summary.json`。这些运行产物被 git 忽略；重新运行会产生新的独立
目录。报告也保存各配置和被映射实现文件的散列，便于识别代码或模型是否已改变。

| 正向配置 | 有限范围 | 生成状态数 | 不同状态数 | 待检查队列 | 结果 |
| --- | --- | ---: | ---: | ---: | --- |
| `TaskLease` | 1 task、2 Workers、version 0–3 | 4,973 | 1,493 | 0 | 六个不变量全部通过 |
| `TwoTasks` | 2 tasks、2 Workers、version 0–2 | 1,374,101 | 207,025 | 0 | 六个不变量全部通过 |

两次检查合计覆盖 208,518 个不同状态，检查耗时约 1.0 秒和 8.0 秒。时间仅描述本机
运行，不是性能承诺。TLC 使用状态指纹；这个结果仍依赖模型、抽象和检查工具本身，
不是经独立证明检查器验证的证明证书。

## 故障变体产生的反例

这些开关仅存在于模型，不修改生产实现。下面记录实际 TLC 输出中的轨迹。

### 删除 finalize 的版本条件

`NoFinalizeFence.cfg` 将 `FenceFinalize` 设为 FALSE，仍保留 Worker ID 检查。
TLC 在第 6 个状态报告 `NoStaleFinalize` 违反，退出码 12。

| 步骤 | 动作 | 当前 Worker／版本 | 资源版本 |
| --- | --- | --- | ---: |
| 1 | 初始 pending | 无／0 | 无 |
| 2 | w1 领取 | w1／1 | 1 |
| 3 | 租约过期 | w1／1 | 1 |
| 4 | w1 再次领取 | w1／2 | 2 |
| 5 | 版本 1 的旧请求开始 finalize，结果为 pending | w1／2 | 2 |
| 6 | 旧请求提交，清空新尝试的所有权和资源 | 无／2 | 无 |

这说明仅检查 Worker ID 不足以隔离两个执行尝试。实际 SQL 保留版本条件，因此
正向模型拒绝第 5 步。

### 删除暂停／取消保护

`OverwriteControl.cfg` 将 `PreserveControl` 设为 FALSE。轨迹为：

```text
pending → w1 领取 → paused → 收到结果 pending → finalize 将 paused 改成 pending
```

TLC 在第 5 个状态报告 `ControlWins` 违反，退出码 12。实际 SQL 中保留当前控制状态
的 CASE 表达式阻止该转换。模型同样枚举 cancelled 和其他结果，记录的最短反例是 paused。

### 释放资源时遗漏任务范围

`ReleaseOtherTasks.cfg` 将 `ReleaseOnlyOwn` 设为 FALSE。轨迹为：

```text
t1 进入 ready 并保留资源 → w1 领取 t2 → 释放 t2 时也清空 t1 的资源
```

t1 仍然是 ready，却没有资源。TLC 在第 4 个状态报告 `ResourcesAccounted` 违反，
退出码 12。当前 SQL 的 `WHERE task_id = p_task_id` 对应模型中的任务范围限制。

## 可达性检查

以下配置保持全部保护开关为 TRUE，但检查刻意为假的“永远到不了目标状态”。
预期反例说明模型确实包含这些路径，而非通过排除它们获得安全检查通过。

| 配置 | 找到的行为 | TLC 退出码 |
| --- | --- | ---: |
| `ReachTakeover` | 同 Worker 的旧租约过期后重新领取，资源属于更高版本 | 12 |
| `ReachReady` | pending → ready → running | 12 |
| `ReachResume` | 领取 → paused → resume；新 fence 与旧资源版本不同 | 12 |

## PostgreSQL 实现对照

运行了现有 Docker PostgreSQL smoke harness 中的八个相关场景，全部通过：

| 测试组 | 场景 |
| --- | --- |
| `TestTaskLifecycleRegressionsSmoke` | `cancel_must_survive_late_success_finalization` |
| 同上 | `pause_survives_late_result_and_resume_invalidates_old_attempt` |
| 同上 | `same_worker_old_lease_cannot_renew_or_finalize_new_attempt` |
| `TestTaskTagConcurrencySmoke` | `tag_changes_keep_attempt_snapshot_and_stale_finalize_is_fenced` |
| 同上 | `expiry_uses_owner_ttl_and_expired_renewal_cannot_revive_permits` |
| 同上 | `resume_retains_invalidated_permits_until_expiry` |
| `TestReadyTaskTransitionsSmoke` | `ready_attribute_edits_release_old_reservation` |
| 同上 | `pause_resume_fences_execution_without_replacing_snapshot` |

执行命令：

```sh
go test -tags=smoke ./pkg/taskcore/e2e \
  -run '^TestTaskLifecycleRegressionsSmoke/(cancel_must_survive_late_success_finalization|pause_survives_late_result_and_resume_invalidates_old_attempt|same_worker_old_lease_cannot_renew_or_finalize_new_attempt)$' \
  -count=1 -v -timeout=120s

go test -tags=smoke ./pkg/taskcore/e2e \
  -run '^(TestTaskTagConcurrencySmoke|TestReadyTaskTransitionsSmoke)/(tag_changes_keep_attempt_snapshot_and_stale_finalize_is_fenced|expiry_uses_owner_ttl_and_expired_renewal_cannot_revive_permits|resume_retains_invalidated_permits_until_expiry|ready_attribute_edits_release_old_reservation|pause_resume_fences_execution_without_replacing_snapshot)$' \
  -count=1 -v -timeout=120s
```

这些测试为实现映射提供独立证据，没有自动回放全部模型轨迹，也不能代替 refinement
证明。本轮没有重跑完整 `make test` 或容器 chaos。
