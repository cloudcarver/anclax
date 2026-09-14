# Anclax 中的异步任务

[English](async-tasks-technical.md) | 中文

> 🚀 **异步任务新手？** 从[教程指南](async-tasks-tutorial.zh.md)开始，了解分步用法。
>
> ⚖️ **需要调度机制细节？** 查看[调度与运行时配置指南](async-task-scheduling-runtime-config.zh.md)，了解 strict/normal 通道语义、`WithPriority`/`WithWeight` 与运行时传播流程。
>
> 跨 Worker、多个 task tag 的并发限制，参见[按 tag 限制全局并发](async-task-tag-concurrency.zh.md)。

本文档提供了 Anclax 异步任务系统的全面概述，涵盖用户体验流程和底层技术机制。

## 目录

- [概述](#概述)
- [用户体验流程](#用户体验流程)
- [底层架构](#底层架构)
- [任务生命周期](#任务生命周期)
- [调度：Priority、Weight 与运行时配置](#调度priorityweight-与运行时配置)
- [高级功能](#高级功能)
- [性能和可靠性](#性能和可靠性)

## 概述

Anclax 的异步任务系统提供了一种强大、可靠的方式来执行后台工作，具有至少一次交付保证。该系统围绕一个简单的原则设计：声明式定义任务，在代码中实现它们，让框架处理排队、重试和监控的所有复杂性。

### 开发者指南（开始之前必读）

如果你要扩展或排查异步任务，请先掌握下面的顶层知识点和检查顺序。
这能避免在分散的路径和假设中来回搜索。

**首先要理解的内容：**
- **任务定义与生成**：任务在规范中声明，然后生成代码。务必确认哪些是生成的，哪些是手写的。
- **运行时职责**：任务入队、工作者执行、重试决策、事件记录是分离的职责。
- **持久化契约**：任务和事件落库，状态流转与重试均由数据驱动，而非内存状态。

**推荐检查顺序：**
1. **规范与配置**：任务定义、重试策略默认值、超时设置、生成配置。
2. **生成接口**：TaskRunner 与 Executor 的 API 作为层间契约。
3. **任务存储层**：入队、状态更新、辅助工具（例如等待任务完成）。
4. **工作者生命周期**：领取/锁定、执行、失败处理、重试逻辑。
5. **事件与钩子**：TaskError 事件如何产生，失败钩子何时触发。
6. **数据库查询**：任务选择、重试与事件查询的权威行为。
7. **测试与示例**：验证行为假设并发现边界条件。

**指导原则：**
- 将生成代码视为层间契约，避免手工修改。
- 将 SQL 与规范视为持久化与类型的真源。
- 若行为不明确，从数据流（任务记录 → 工作者 → 事件）入手，而不是寻找单一入口。

### 主要优势

- **至少一次交付**：任务保证至少成功执行一次
- **自动重试**：失败的任务根据可配置的策略进行重试
- **类型安全**：任务参数的完整编译时类型检查
- **事务支持**：任务可以在数据库事务中排队
- **Cron 调度**：任务可以使用 cron 表达式按计划运行
- **失败钩子**：任务永久失败时的自动清理和通知

## 用户体验流程

### 1. 任务定义阶段

用户首先在 `api/tasks/tasks.yaml` 中使用声明式 YAML 格式定义任务：

```yaml
tasks:
  - name: SendWelcomeEmail
    description: 向新用户发送欢迎邮件
    parameters:
      type: object
      required: [userId, templateId]
      properties:
        userId:
          type: integer
          format: int32
        templateId:
          type: string
    retryPolicy:
      interval: 5m
      maxAttempts: 3
    timeout: 30s
```

**幕后发生的事情：**
- 框架验证 YAML 模式
- 任务定义被解析并存储用于代码生成
- 参数模式根据 JSON Schema 标准进行验证

### 2. 代码生成阶段

用户运行 `anclax gen` 来生成类型安全的接口：

```bash
anclax gen
```

**幕后发生的事情：**
- 代码生成器读取所有任务定义
- 生成强类型参数结构体
- 为每个任务创建带有方法的 `ExecutorInterface`
- 创建用于排队任务的 `TaskRunner` 接口
- 生成用于测试的模拟实现

**生成的代码示例：**
```go
// 生成的参数结构体
type SendWelcomeEmailParameters struct {
    UserId     int32  `json:"userId"`
    TemplateId string `json:"templateId"`
}

// 生成的执行器接口
type ExecutorInterface interface {
    ExecuteSendWelcomeEmail(ctx context.Context, tx pgx.Tx, params *SendWelcomeEmailParameters) error
}

// 生成的任务运行器接口
type TaskRunner interface {
    RunSendWelcomeEmail(ctx context.Context, params *SendWelcomeEmailParameters, overrides ...taskcore.TaskOverride) (int32, error)
    RunSendWelcomeEmailWithTx(ctx context.Context, tx pgx.Tx, params *SendWelcomeEmailParameters, overrides ...taskcore.TaskOverride) (int32, error)
}
```

### 3. 实现阶段

用户实现生成的执行器接口：

```go
func (e *Executor) ExecuteSendWelcomeEmail(ctx context.Context, tx pgx.Tx, params *taskgen.SendWelcomeEmailParameters) error {
    user, err := e.model.GetUser(ctx, params.UserId)
    if err != nil {
        return err
    }
    
    return e.emailService.SendWelcomeEmail(user.Email, params.TemplateId)
}
```

**幕后发生的事情：**
- 执行器在任务工作者系统中注册
- 框架将任务类型映射到执行器方法
- 在方法调用前自动进行参数验证

### 4. 任务执行阶段

用户从应用程序代码中触发任务：

```go
// 从 HTTP 处理器
func (h *Handler) RegisterUser(c *fiber.Ctx) error {
    // ... 用户注册逻辑 ...
    
    // 排队欢迎邮件任务
    taskID, err := h.taskRunner.RunSendWelcomeEmail(c.Context(), &taskgen.SendWelcomeEmailParameters{
        UserId:     user.ID,
        TemplateId: "welcome",
    })
    
    if err != nil {
        return err
    }
    
    return c.JSON(fiber.Map{"taskId": taskID})
}
```

**幕后发生的事情：**
- 任务参数序列化为 JSON
- 任务记录插入数据库
- 任务标记为 `pending`
- 立即返回任务 ID
- 后台工作者接收并执行任务

## 底层架构

任务状态保存在 `anclax.tasks`，事件、Worker 注册信息和调度配置分别保存在 `anclax.events`、`anclax.workers`、`anclax.worker_runtime_configs`。Cron 元数据保存在任务属性和 `started_at` 中。数据库结构以 `sql/migrations` 为准。

Worker 分为四个职责边界：

- `Engine` 是纯 `Event -> []Command` 状态机，负责并发准入、严格通道容量、标签组加权轮转和手动执行请求。
- `Runtime` 负责事件循环、定时器、异步操作、取消和有时限的停机收尾。自动拉取与 `RunTask` 共用并发预算，直到 finalization 完成才释放容量。
- `ModelPort` 执行短数据库操作，并在事务外调用处理器。执行注册表以“任务 ID + 租约版本”为键。
- 生命周期策略负责无 I/O 的结果计算；生命周期处理器在同一事务中持久化状态、事件并调用失败钩子。

每次轮询会填满可用业务槽位，任务收尾后立即补位；领取不到任务时等待下一次轮询。默认业务并发为 10。框架控制任务每个 Worker 另有一个独立槽位。

### 事务安全

在 `model.RunTransactionWithTx` 中使用生成的 `Run*WithTx` 方法，可以让业务变更和任务入队一起提交。执行器运行期间不持有框架数据库事务；失败钩子通过 `core.Tx` 接收收尾事务。

## 任务生命周期

### 领取、执行与收尾

1. 使用 `FOR UPDATE SKIP LOCKED` 领取到期的 `pending` 任务，同时检查标签、串行顺序、优先级和标签组。租约过期判断使用数据库时间。
2. 设置 `worker_id`、`locked_at`，递增 `lease_version` 和 `attempts`，提交后执行。持有租约的任务在数据库中仍为 `pending`，通过租约标识执行状态。
3. 使用可取消的上下文和可选任务超时调用处理器，运行期间续租。执行器 panic 会进入失败处理路径。
4. 根据任务 ID、Worker ID 和租约版本原子收尾，释放租约并记录相应事件。已经提交的暂停/取消优先于晚到的执行结果；旧租约不能写回。

普通任务成功后变为 `completed`。失败且还有重试预算时，保持 `pending` 并推迟 `started_at`；否则变为 `failed`。`completed`、`failed`、`cancelled` 是终态。恢复只作用于暂停任务，并使旧执行失效；如果旧租约尚未释放，恢复后的任务可能需要等待租约过期才能重新领取。

### 重试与持久化延期

重试间隔是固定的正 Go duration，例如 `5s`、`1m`。`maxAttempts` 包含首次执行，负数表示无限重试。`ErrFatalTask` 跳过重试；`ErrRetryTaskWithoutErrorEvent` 在继续重试时不记录错误事件。

返回 `taskcore.DeferTask(delay)` 会持久化下一次调用时间，释放执行槽位，不消耗尝试次数，也不产生失败事件。延期前已经执行的操作必须支持幂等。Worker 停机中断也使用此重新调度路径；普通任务超时仍按失败处理。

### Cron 生命周期

Cron 任务复用同一数据库行和任务 ID。重试预算属于当前这一轮执行。成功或重试耗尽后，按六字段 Cron 表达式安排下一轮，并将 `attempts` 归零。即使没有重试策略，单轮失败也会记录错误、调用失败钩子并保留后续调度。暂停和取消会停止后续调度。

### 失败钩子与停机

`OnTaskFailed` 在当前执行轮次重试耗尽或返回致命错误时调用。Savepoint 隔离钩子的 SQL 错误和 panic：回滚钩子变更后，任务结果和事件仍可提交。数据库或提交本身的错误仍会上报。

停机时停止接收任务、取消执行器，并使用不受调用方取消影响的上下文完成收尾。数据库操作与停机等待默认各有五秒时限。Worker 离线标记在已发出的操作结束后写入，包括启动注册和心跳。忽略取消信号的执行器可能超过该时限，任务依靠租约过期恢复。交付语义仍为至少一次，外部副作用需要幂等处理。

## 调度：Priority、Weight 与运行时配置

### 通道语义

内置配置更新、暂停、取消及其广播任务使用独立控制通道；以下优先级规则适用于业务任务。

- **严格通道（strict lane）**：`priority > 0`
  - 在 strict 槽可用时优先领取
  - 排序：`priority DESC`，然后 `created_at ASC`，然后 `id ASC`
- **普通通道（normal lane）**：`priority == 0`
  - 通过标签组加权轮转选择
  - 组间公平由运行时 `labelWeights` 控制

严格通道容量受以下公式约束：

```text
strict_cap = ceil(concurrency * maxStrictPercentage / 100)
```

### 任务级控制

- `taskcore.WithPriority(priority int32)`
  - 校验 `priority >= 0`
- `taskcore.WithWeight(weight int32)`
  - 校验 `weight >= 1`
  - 在普通通道已选组内影响领取顺序（`weight DESC`）

### 运行时 worker 配置更新

内置任务 `broadcastUpdateWorkerRuntimeConfig` 会写入版本化配置，并向存活 worker 快照 fanout worker-control 命令任务。

流程摘要：
1. 根据请求 ID 在 `anclax.worker_runtime_configs` 中幂等取得或创建版本。
2. 为每个远端目标 worker 入队 `applyWorkerRuntimeConfigToWorker`；本地 worker 可直接触发。
3. worker 通过 `worker:<id>` 标签领取自己的命令任务，刷新最新配置、原子应用，并单调更新 `workers.applied_config_version`。
4. 收敛以 DB 的落后 worker 状态为准。
5. 如果等待期间出现更新版本，旧广播任务会视为 superseded 并退出。

可运行示例与运维说明：
- [调度与运行时配置指南](async-task-scheduling-runtime-config.zh.md)
- [异步任务 Worker 租约设计](async-task-worker-lease.md)
- [异步任务生产就绪测试策略](async-task-testing-production-readiness.md)

### Worker 控制任务请求

Worker 控制面消息使用持久化任务中的保留类型，由独立控制通道领取：

- `broadcastUpdateWorkerRuntimeConfig` fanout `applyWorkerRuntimeConfigToWorker`。
- `broadcastCancelTask` fanout `cancelTaskOnWorker`。
- `broadcastPauseTask` fanout `pauseTaskOnWorker`。

广播任务会快照存活 worker，为每个远端 worker 入队一个定向命令任务，并根据操作等待命令任务完成或 DB 收敛。定向命令任务使用 `worker:<id>` 标签和 unique tag，确保目标 worker 领取自己的命令。

调用 `InterruptTasks` 后，控制任务检查目标执行是否已经完成收尾。尚未完成时返回 `DeferTask`，持久化下次检查时间并释放控制槽位；广播等待远端确认也使用同样机制。执行注册表在 `FinalizeTask` 结束时移除相应租约版本。控制面调用方仍等待收敛，Worker 的控制槽位不会被等待占住。延期沿用稳定的请求 ID、配置版本和每个目标 Worker 的子任务 unique tag。

新增 worker-control 请求时：
1. **定义任务 schema**：修改 `api/tasks/tasks.yaml`。
2. **重新生成**：运行 `anclax gen` 更新生成代码。
3. **新增广播执行逻辑**：快照目标 worker 并入队 worker 定向命令任务。
4. **Worker 处理**：在 `WorkerControlTaskHandler` 中路由处理，并同步 `worker.IsControlTask` 和 SQL 领取查询中的控制类型列表。
5. **补充测试**：覆盖 fanout、本地 worker fast path、目标 worker 过滤、重复命令行为，以及等待/收敛语义。

## 高级功能

### 任务覆盖

任务行为的运行时自定义：

```go
taskID, err := taskRunner.RunSendWelcomeEmail(ctx, params,
    taskcore.WithRetryPolicy("1h", 5),           // 自定义重试
    taskcore.WithTimeout("2m"),                  // 自定义超时
    taskcore.WithUniqueTag("user-123-welcome"),  // 防止重复
    taskcore.WithParentTaskID(parentID),         // 关联父任务
    taskcore.WithDelay(time.Hour),               // 延迟执行
)
```

**覆盖实现：**
- 覆盖作为函数选项应用
- 它们在数据库插入前修改任务属性
- 类型安全验证确保覆盖兼容性

### 任务层级与控制面中断

任务可以通过可选的 `parentTaskId` 形成层级。入队子任务时使用 `taskcore.WithParentTaskID`。当控制面收到 `PauseTask` 或 `CancelTask` 请求时，会在同一事务内对目标任务及其所有后代任务应用状态更新，并一次性入队包含全部任务 ID 的 interrupt task。

### 等待任务完成

有时需要阻塞直到任务完成（用于测试、编排或 CLI 工作流）。
WorkerControlPlane 提供了等待辅助方法，用于等待终态并附带失败上下文。

```go
ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()

err := controlPlane.WaitForTask(ctx, taskID)
```

**工作机制：**
- 等待任务进入 `completed`、`failed` 或 `cancelled`。
- 注册等待时不查询数据库。共享 listener 在下一轮查询中统一检查任务存在性及状态，每批最多 256 个 ID，正常轮询间隔为一秒。任务已经完成或不存在，也通过 channel 异步返回。
- 每批查询超时为五秒；瞬时数据库错误保留订阅并指数退避重试，最长间隔 30 秒；永久性 PostgreSQL 错误返回给等待者。没有订阅时不查询。
- 失败时读取最新的 TaskError 事件，并返回包含以下信息的错误消息：
  - 任务尝试次数
  - 重试策略的最大尝试次数
  - TaskError 事件中的最新错误消息
- 取消时返回包装了 `ErrTaskCancelled` 的错误。
- 超时或上下文取消时，直接返回上下文错误。
- 在任务 handler 内等待仍占用 worker 执行槽位。应在业务事务提交后等待其中创建的任务，框架不会自动提交事务。

**续租连接池：**
- 每个 worker 统一调度续租，每批最多 256 个任务，保留逐任务租约版本校验和失租中断。
- 标准 model 使用独立的续租连接池，续租及失败后的状态核查均走该池；共享同一 model 的 worker 共用此池。
- `worker.leaseRenewalMaxConnections` 设置续租池最大连接数，默认 10，必须大于零。连接按需建立，额度独立于业务池的 `LibConfig.Pg.MaxConnections`。

**实现参考：**
- `pkg/taskcore/ctrl/ctrl.go` 实现公开等待辅助方法。
- `pkg/taskcore/listener` 实现内部 task listener。
- `sql/queries/tasks.sql` 定义 wait status 和 task error 查询。

### 失败钩子

自动清理和通知系统：

```yaml
tasks:
  - name: ProcessPayment
    # ... 其他配置 ...
    events:
      - onFailed
```

**钩子机制：**
- 钩子在普通任务永久失败或 Cron 当前轮次失败时触发
- 钩子接收原始任务参数，具有完全的类型安全性
- 钩子在与状态更新相同的事务中执行，并通过 savepoint 隔离错误
- 钩子失败会记录但不影响任务状态

### 唯一任务

防止重复任务执行：

```go
// 这会成功
taskID1, _ := taskRunner.RunSendWelcomeEmail(ctx, params,
    taskcore.WithUniqueTag("user-123-welcome"))

// 这会失败，返回 ErrTaskAlreadyExists
taskID2, _ := taskRunner.RunSendWelcomeEmail(ctx, params,
    taskcore.WithUniqueTag("user-123-welcome"))
```

**唯一性实现：**
- 唯一标签存储在数据库中，具有唯一约束
- 重复检测在数据库级别发生
- 失败的重复返回特定错误类型

## 性能和可靠性

### 可扩展性特征

- **水平扩展**：多个应用程序实例可以运行工作者
- **数据库瓶颈**：所有协调通过数据库进行
- **轮询开销**：工作者每秒轮询（可配置）
- **内存使用**：最小 - 任务不保存在内存中

### 可靠性保证

- **至少一次交付**：通过数据库持久化和重试逻辑保证
- **崩溃恢复**：任务在应用程序重启后存活
- **事务安全**：任务排队遵守事务边界
- **超时保护**：失控任务被终止

### 监控和可观察性

系统公开监控指标：

```go
// Prometheus 指标
var (
    TasksEnqueued = promauto.NewCounter(...)
    TasksCompleted = promauto.NewCounter(...)
    TasksFailed = promauto.NewCounter(...)
    TaskExecutionDuration = promauto.NewHistogram(...)
)
```

**可用指标：**
- 任务排队率
- 任务完成率  
- 任务失败率
- 执行持续时间分布
- 队列深度
- 工作者利用率

### 最佳实践

1. **设计幂等性**
   - 任务可能被多次执行
   - 使用数据库事务或唯一约束
   - 在进行更改前检查当前状态

2. **处理部分失败**
   - 将大任务分解为较小的单元
   - 对复杂工作流使用 saga 模式
   - 实现适当的回滚逻辑

3. **监控和告警**
   - 为高失败率设置告警
   - 监控队列深度以进行容量规划
   - 跟踪执行时间以发现性能回归

4. **测试失败场景**
   - 在各种失败条件下测试重试行为
   - 验证失败钩子正常工作
   - 确保优雅降级

5. **资源管理**
   - 设置适当的超时
   - 限制并发任务执行
   - 监控内存和 CPU 使用

6. **使用异步任务解耦模块**
   - 通过使用异步任务而不是直接方法调用来解耦模块
   - 例如，当订单支付完成时，不要在 `finishOrder()` 中直接调用所有工厂操作，而是排队一个 `orderFinished` 任务
   - 这保持了 `finishOrder` 方法的简洁性，并允许工厂特定的逻辑在工厂模块内定义
   - 产生更清洁的代码，更容易调试和维护
   - **重要提示**：仅在最终一致性场景中使用此模式，不适用于强一致性要求，如账户间的实时金融交易

```go
// 不要这样做（紧耦合）：
func (o *OrderService) FinishOrder(ctx context.Context, orderID int32) error {
    // 更新订单状态
    if err := o.model.UpdateOrderStatus(ctx, orderID, "completed"); err != nil {
        return err
    }
    
    // 直接调用工厂操作（紧耦合）
    if err := o.factoryService.StartProduction(ctx, orderID); err != nil {
        return err
    }
    if err := o.factoryService.AllocateResources(ctx, orderID); err != nil {
        return err
    }
    if err := o.factoryService.ScheduleDelivery(ctx, orderID); err != nil {
        return err
    }
    
    return nil
}

// 应该这样做（使用异步任务解耦）：
func (o *OrderService) FinishOrder(ctx context.Context, orderID int32) error {
    return o.model.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
        // 更新订单状态
        if err := o.model.UpdateOrderStatus(ctx, orderID, "completed"); err != nil {
            return err
        }
        
        // 为工厂操作排队异步任务（松耦合）
        _, err := o.taskRunner.RunOrderFinishedWithTx(ctx, tx, &taskgen.OrderFinishedParameters{
            OrderId: orderID,
        })
        
        return err
    })
}

// 工厂模块独立处理自己的逻辑
func (f *FactoryExecutor) ExecuteOrderFinished(ctx context.Context, tx pgx.Tx, params *taskgen.OrderFinishedParameters) error {
    // 所有工厂特定逻辑都包含在工厂模块内
    if err := f.startProduction(ctx, params.OrderId); err != nil {
        return err
    }
    if err := f.allocateResources(ctx, params.OrderId); err != nil {
        return err
    }
    if err := f.scheduleDelivery(ctx, params.OrderId); err != nil {
        return err
    }
    
    return nil
}
```

这个综合系统为异步任务处理提供了强大的基础，同时通过其声明式配置和类型安全接口为开发者保持简单性。
