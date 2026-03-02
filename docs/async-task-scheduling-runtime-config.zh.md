# 异步任务调度：Priority、Weight 与运行时 Worker 配置

[English](async-task-scheduling-runtime-config.md) | 中文

本文说明 Anclax 如何通过“严格优先级 + 加权公平”调度异步任务，以及如何在**不重启 worker** 的情况下在线更新调度配置。

## 目录

- [快速概览](#快速概览)
- [任务分类与选择顺序](#任务分类与选择顺序)
- [如何使用 `WithPriority` 和 `WithWeight`](#如何使用-withpriority-和-withweight)
- [运行时 Worker 配置任务](#运行时-worker-配置任务)
- [传播流程（LISTEN/NOTIFY + DB）](#传播流程listennotify--db)
- [运维说明](#运维说明)
- [参考文档](#参考文档)

## 快速概览

- `priority > 0` => **严格通道（strict lane）**。
- `priority == 0` => **普通通道（normal lane）**。
- 严格通道受 `maxStrictPercentage` 与 worker 并发数共同限制。
- 普通通道通过运行时标签组权重（`labelWeights`）实现加权公平。
- 在选中的普通组内，任务按 `weight DESC`，再按 `created_at`、`id` 排序。
- 运行时配置通过 DB 版本号 + Postgres `LISTEN/NOTIFY` + worker ACK 状态传播。

## 任务分类与选择顺序

### 1）严格通道（`priority > 0`）

- worker 会优先尝试领取严格通道任务。
- 严格通道内排序：
  1. `priority DESC`
  2. `created_at ASC`
  3. `id ASC`
- 只有当 `strict_inflight < strict_cap` 时，才允许领取严格任务。

严格容量计算：

```text
strict_cap = ceil(concurrency * maxStrictPercentage / 100)
```

边界行为：
- `maxStrictPercentage <= 0` => `strict_cap = 0`
- `maxStrictPercentage >= 100` => `strict_cap = concurrency`

### 2）普通通道（`priority == 0`）

- 当严格槽不可用或无可领取严格任务时，进入普通通道。
- worker 基于运行时 `labelWeights` 构建加权轮盘，轮转选择组。
- 每个普通任务会映射到一个组：
  - 若任务标签与配置标签有交集：取**字典序最小**标签。
  - 否则进入 `default` 组。
- 组公平性是每个 worker 本地近似，集群全局公平是近似值。

在选中组内，SQL 领取顺序为：
1. `weight DESC`
2. `created_at ASC`
3. `id ASC`

## 如何使用 `WithPriority` 和 `WithWeight`

`taskcore` 覆盖项：

- `taskcore.WithPriority(priority int32)`
  - 校验：`priority >= 0`
- `taskcore.WithWeight(weight int32)`
  - 校验：`weight >= 1`

示例：

```go
taskID, err := taskRunner.RunSendWelcomeEmail(ctx, params,
    taskcore.WithPriority(10), // 严格通道，紧急任务
    taskcore.WithWeight(3),    // 普通组内更靠前
)
```

建议：
- `WithPriority` 只用于少量真正紧急流量（事故处置、管理控制面任务等）。
- 大多数业务任务保持 `priority=0`，通过运行时 `labelWeights` 做组间公平，必要时配合任务 `weight` 做组内排序。

## 运行时 Worker 配置任务

Anclax 提供 `updateWorkerRuntimeConfig` 任务（定义见 `api/tasks.yaml`），用于在线更新调度配置。

生成参数（`taskgen.UpdateWorkerRuntimeConfigParameters`）：

- `requestID`（可选）：关联 ID；为空时自动生成。
- `maxStrictPercentage`（可选）：`[0, 100]`。
- `defaultWeight`（可选）：`>= 1`。
- `labels` + `weights`（可选）：长度必须相同。
- `notifyInterval`（可选）：例如 `"1s"`，必须为正。
- `listenTimeout`（可选）：例如 `"2s"`，必须为正。

说明：控制面会自动提供 `requestID`、`notifyInterval` 和 `listenTimeout`；调用方无需手动设置。

### 使用 Worker 控制面

建议通过控制面 API 入队并等待配置更新完成：

```go
import "github.com/cloudcarver/anclax/pkg/taskcore/ctrl"

maxStrict := int32(20)
defaultWeight := int32(1)
labels := []string{"w1", "w2"}
weights := []int32{5, 1}

controlPlane := ctrl.NewWorkerControlPlane(model, taskRunner, taskStore)
err := controlPlane.UpdateWorkerRuntimeConfig(ctx,
    &ctrl.UpdateWorkerRuntimeConfigRequest{
        MaxStrictPercentage: &maxStrict,
        DefaultWeight:       &defaultWeight,
        Labels:              labels,
        Weights:             weights,
    },
)
```

推荐原因：
- 控制面始终将配置更新任务设为保留最高严格优先级（`math.MaxInt32`）。
- 避免控制面配置更新被低优先级业务任务长期阻塞。
- 调用方无需关心 request ID 与 LISTEN/NOTIFY 细节；控制面负责重试与 ACK 等待。

## 传播流程（LISTEN/NOTIFY + DB）

### 写入侧（配置更新任务执行器）

1. 校验并规范化参数。
2. 向 `anclax.worker_runtime_configs` 插入新版本。
3. 向 `anclax_worker_runtime_config` 发送通知：
   - `op: "up_config"`
   - `{request_id, version}`
4. 循环直到收敛或被新版本覆盖：
   - 查询落后但仍存活的 worker（`applied_config_version < target_version`）
   - 重发通知
   - 可选监听 ACK 频道 `anclax_worker_runtime_config_ack`

### Worker 侧

1. 启动运行时配置循环（先 `LISTEN`，再从 DB 拉取最新）。
2. 收到通知（或轮询触发）后拉取最新版本。
3. 若版本更新：原子更新内存配置，并用单调 `GREATEST` 更新 worker 行。
4. 发送 ACK：`{request_id, worker_id, applied_version}`。

### 收敛判定的真源

- 通知/ACK 仅用于加速唤醒。
- **DB 状态才是最终真源**：
  - 当没有存活 worker 落后目标版本时视为收敛。
- 若等待期间出现更新版本，旧版本任务会视为 superseded 并提前退出。

## 运维说明

- 启动默认严格比例可来自 `worker.maxStrictPercentage`（应用配置）。
- 运行时更新可在不重启 worker 的前提下生效。
- 可选 `worker.runtimeConfigPollInterval` 作为通知异常时的兜底轮询。
- 公平性是 worker 本地近似，建议结合真实流量验证 SLO。

可用指标：
- `anclax_worker_strict_inflight`
- `anclax_worker_strict_cap`
- `anclax_worker_strict_saturation_total`
- `anclax_worker_runtime_config_version`
- `anclax_runtime_config_lagging_workers`
- `anclax_runtime_config_convergence_seconds`
- `anclax_runtime_config_superseded_total`

## 参考文档

- 总览与架构：[async-tasks-technical.zh.md](async-tasks-technical.zh.md)
- 上手教程：[async-tasks-tutorial.zh.md](async-tasks-tutorial.zh.md)
- Worker 租约模型：[async-task-worker-lease.md](async-task-worker-lease.md)
- 生产就绪测试策略：[async-task-testing-production-readiness.md](async-task-testing-production-readiness.md)
