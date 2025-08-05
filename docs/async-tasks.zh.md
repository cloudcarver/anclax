# Anchor 中的异步任务

[English](async-tasks.md) | 中文

Anchor 让您可以运行不阻塞 Web 请求的后台任务。例如，您可以发送电子邮件、处理图像或生成报告，而无需让用户等待。

## 目录

- [什么是异步任务？](#什么是异步任务)
- [如何创建任务](#如何创建任务)
- [如何编写任务代码](#如何编写任务代码)
- [如何启动任务](#如何启动任务)
- [定时任务（Cronjobs）](#定时任务cronjobs)
- [错误处理和钩子](#错误处理和钩子)
- [完整示例](#完整示例)

## 什么是异步任务？

将异步任务想象成雇佣某人稍后为您做工作。您可以：

1. **创建任务** - 告诉 Anchor 需要完成什么工作
2. **排队** - 将任务放入待办事项列表
3. **让工作者处理** - 后台工作者接收任务并完成工作
4. **获得保证** - 任务将至少运行一次，即使出现问题

常见示例：
- 用户注册时发送欢迎邮件
- 上传后调整图像大小
- 生成月度报告
- 清理旧数据
- 处理付款

## 任务定义

任务在 `api/tasks.yaml` 中使用结构化的 YAML 格式定义：

```yaml
tasks:
  - name: TaskName
    description: "任务描述"
    parameters:
      type: object
      required: [param1, param2]
      properties:
        param1:
          type: string
          description: "参数描述"
        param2:
          type: integer
          format: int32
    retryPolicy:
      interval: 30m
      maxAttempts: -1
    cronjob:
      cronExpression: "0 */1 * * *"  # 每小时
    events:
      - onFailed
    timeout: 10m
```

### 任务属性

- **name**（必需）：唯一任务标识符
- **description**：人类可读的任务描述
- **parameters**：定义任务参数的 JSON Schema
- **retryPolicy**：失败任务的重试配置
- **cronjob**：Cron 调度配置
- **events**：生命周期钩子数组（例如 `[onFailed]`）
- **timeout**：最大执行时间（默认：1 小时）

### 参数类型

参数遵循 JSON Schema 格式：
```yaml
parameters:
  type: object
  required: [userId, amount]
  properties:
    userId:
      type: integer
      format: int32
    amount:
      type: number
      format: float
    metadata:
      type: object
    tags:
      type: array
      items:
        type: string
```

## 任务实现

定义任务后，运行代码生成：

```bash
anchor generate
```

这会在 `pkg/zgen/taskgen/` 中生成接口：

### 生成的接口

```go
// ExecutorInterface - 实现此接口以处理任务执行和钩子
type ExecutorInterface interface {
    // 执行主要任务
    ExecuteTaskName(ctx context.Context, params *TaskNameParameters) error
    
    // 任务永久失败时调用的钩子（如果配置了 events: [onFailed]）
    OnTaskNameFailed(ctx context.Context, taskID int32, params *TaskNameParameters, tx pgx.Tx) error
}

// TaskRunner - 使用此接口来排队任务
type TaskRunner interface {
    RunTaskName(ctx context.Context, params *TaskNameParameters, overrides ...taskcore.TaskOverride) (int32, error)
    RunTaskNameWithTx(ctx context.Context, tx pgx.Tx, params *TaskNameParameters, overrides ...taskcore.TaskOverride) (int32, error)
}

// Hook - 自动生成的钩子调度器
type Hook interface {
    OnTaskFailed(ctx context.Context, tx pgx.Tx, failedTaskSpec TaskSpec, taskID int32) error
}
```

### 实现执行器

创建一个实现生成接口的执行器：

```go
package asynctask

import (
    "context"
    "pkg/zgen/taskgen"
    "pkg/zcore/model"
)

type Executor struct {
    model model.ModelInterface
}

func NewExecutor(model model.ModelInterface) taskgen.ExecutorInterface {
    return &Executor{
        model: model,
    }
}

func (e *Executor) ExecuteTaskName(ctx context.Context, params *taskgen.TaskNameParameters) error {
    // 您的任务逻辑在这里
    return e.model.DoSomething(ctx, params.UserId, params.Amount)
}
```

## 运行任务

### 排队任务

使用生成的 TaskRunner 来排队任务：

```go
func (h *Handler) EnqueueTask(c *fiber.Ctx) error {
    params := &taskgen.TaskNameParameters{
        UserId: 123,
        Amount: 50.0,
    }
    
    taskID, err := h.taskRunner.RunTaskName(c.Context(), params)
    if err != nil {
        return err
    }
    
    return c.JSON(fiber.Map{"taskId": taskID})
}
```

### 任务覆盖

您可以在运行时覆盖任务属性：

```go
// 覆盖重试策略
taskID, err := h.taskRunner.RunTaskName(ctx, params, 
    taskcore.WithRetryPolicy("1h", true),
    taskcore.WithTimeout("30m"),
    taskcore.WithUniqueTag("user-123-daily-task"),
)
```

### 事务性任务

在数据库事务中排队任务：

```go
err := h.model.RunTransaction(ctx, func(txm model.ModelInterface) error {
    // 执行一些数据库工作
    user, err := txm.GetUser(ctx, userID)
    if err != nil {
        return err
    }
    
    // 在同一事务中排队任务
    taskID, err := h.taskRunner.RunTaskNameWithTx(ctx, txm.GetTx(), params)
    if err != nil {
        return err
    }
    
    return nil
})
```

## 定时任务（Cronjobs）

使用 cron 表达式定义定时任务：

```yaml
tasks:
  - name: DailyCleanup
    description: "运行每日清理任务"
    cronjob:
      cronExpression: "0 0 2 * * *"  # 每日凌晨 2 点
    parameters:
      type: object
      properties:
        daysToKeep:
          type: integer
          format: int32
```

定时任务支持扩展的 cron 格式（包含秒）：
- 格式：`second minute hour dayOfMonth month dayOfWeek`
- 示例：`"*/30 * * * * *"`（每 30 秒）
- 示例：`"0 0 */6 * * *"`（每 6 小时）

## 重试策略

配置任务失败时的重试方式：

```yaml
retryPolicy:
  interval: 30m      # 重试间隔等待 30 分钟
  maxAttempts: -1    # 无限重试（-1 表示无限，正数限制尝试次数）
```

### 重试间隔

- 简单持续时间：`"30m"`、`"1h"`、`"5s"`
- 指数退避：`"1m,2m,4m,8m"`（逗号分隔）

## 错误处理和钩子

### 任务失败钩子

任务可以使用 `events` 配置在失败时自动触发钩子方法：

```yaml
tasks:
  - name: ProcessPayment
    description: "处理用户付款"
    parameters:
      type: object
      required: [userId, amount]
      properties:
        userId:
          type: integer
          format: int32
        amount:
          type: number
    retryPolicy:
      interval: 30m
      maxAttempts: -1
    events:
      - onFailed
```

### 钩子工作原理

1. **自动触发**：当任务永久失败时（所有重试后），系统自动调用相应的钩子方法
2. **事务安全**：原始任务状态更新和钩子执行在同一数据库事务中发生
3. **类型参数**：钩子方法接收原始任务参数和任务 ID，具有完全的类型安全性
4. **无重试干扰**：钩子仅在任务永久失败时触发，不在重试期间触发

### 钩子方法签名

当您定义带有 `events: [onFailed]` 的任务时，代码生成器会自动在 `ExecutorInterface` 中创建钩子方法：

```go
type ExecutorInterface interface {
    // 执行主要任务
    ExecuteTaskName(ctx context.Context, params *TaskNameParameters) error
    
    // 任务永久失败时调用的钩子
    OnTaskNameFailed(ctx context.Context, taskID int32, params *TaskNameParameters, tx pgx.Tx) error
}
```

### 实现失败钩子

```go
func (e *Executor) ExecuteProcessPayment(ctx context.Context, params *taskgen.ProcessPaymentParameters) error {
    // 您的付款处理逻辑
    if err := e.paymentService.ProcessPayment(params.UserId, params.Amount); err != nil {
        // 如果重试用尽，此错误将触发 OnProcessPaymentFailed
        return fmt.Errorf("付款处理失败: %w", err)
    }
    return nil
}

func (e *Executor) OnProcessPaymentFailed(ctx context.Context, taskID int32, params *taskgen.ProcessPaymentParameters, tx pgx.Tx) error {
    // 钩子直接接收原始任务参数，具有完全的类型安全性
    log.Error("付款处理永久失败", 
        zap.Int32("taskID", taskID),
        zap.Int32("userId", params.UserId),
        zap.Float64("amount", params.Amount))
    
    // 处理失败（通知管理员、退款等）
    // 事务上下文允许您进行额外的数据库操作
    return e.handlePaymentFailure(ctx, params.UserId, params.Amount, taskID)
}
```

### 自定义错误处理

在执行器中，您可以控制重试行为：

```go
func (e *Executor) ExecuteProcessPayment(ctx context.Context, params *taskgen.ProcessPaymentParameters) error {
    // 永久失败 - 不重试，立即触发 onFailed
    if params.Amount <= 0 {
        return taskcore.ErrFatalTask
    }
    
    // 临时失败 - 重试但不记录错误事件
    if rateLimitExceeded {
        return taskcore.ErrRetryTaskWithoutErrorEvent
    }
    
    // 常规错误 - 将根据策略重试
    return processPayment(params)
}
```

## 高级功能

### 任务超时

配置最大执行时间：

```yaml
tasks:
  - name: LongRunningTask
    timeout: 2h  # 最多 2 小时
```

### 唯一任务

使用唯一标签防止重复任务：

```go
taskID, err := h.taskRunner.RunTaskName(ctx, params, 
    taskcore.WithUniqueTag(fmt.Sprintf("user-%d-daily", userID)),
)
```

### 任务属性

在执行器中访问任务元数据：

```go
func (e *Executor) ExecuteTaskName(ctx context.Context, params *taskgen.TaskNameParameters) error {
    // 从上下文获取任务 ID（如果可用）
    if taskID, ok := ctx.Value("taskID").(int32); ok {
        log.Info("处理任务", zap.Int32("taskID", taskID))
    }
    
    return e.processTask(params)
}
```

## 示例

### 示例 1：简单后台任务

**任务定义：**
```yaml
tasks:
  - name: SendEmail
    description: "向用户发送电子邮件"
    parameters:
      type: object
      required: [userId, templateId]
      properties:
        userId:
          type: integer
          format: int32
        templateId:
          type: string
        variables:
          type: object
    retryPolicy:
      interval: 5m
      maxAttempts: -1
```

**实现：**
```go
func (e *Executor) ExecuteSendEmail(ctx context.Context, params *taskgen.SendEmailParameters) error {
    user, err := e.model.GetUser(ctx, params.UserId)
    if err != nil {
        return err
    }
    
    template, err := e.emailService.GetTemplate(params.TemplateId)
    if err != nil {
        return err
    }
    
    return e.emailService.SendEmail(user.Email, template, params.Variables)
}
```

**使用：**
```go
func (h *Handler) RegisterUser(c *fiber.Ctx) error {
    // ... 用户注册逻辑
    
    // 异步发送欢迎邮件
    _, err := h.taskRunner.RunSendEmail(c.Context(), &taskgen.SendEmailParameters{
        UserId:     user.ID,
        TemplateId: "welcome",
        Variables:  map[string]interface{}{"name": user.Name},
    })
    
    return err
}
```

### 示例 2：定时数据处理

**任务定义：**
```yaml
tasks:
  - name: ProcessDailyReports
    description: "生成每日报告"
    cronjob:
      cronExpression: "0 0 1 * * *"  # 每日凌晨 1 点
    parameters:
      type: object
      required: [date]
      properties:
        date:
          type: string
          format: date
    retryPolicy:
      interval: 1h
      maxAttempts: -1
```

**实现：**
```go
func (e *Executor) ExecuteProcessDailyReports(ctx context.Context, params *taskgen.ProcessDailyReportsParameters) error {
    date, err := time.Parse("2006-01-02", params.Date)
    if err != nil {
        return err
    }
    
    // 处理给定日期的报告
    return e.reportService.GenerateDailyReports(ctx, date)
}
```

### 示例 3：带失败事件的工作流

**任务定义：**
```yaml
tasks:
  - name: ProcessOrder
    description: "处理客户订单"
    parameters:
      type: object
      required: [orderId]
      properties:
        orderId:
          type: integer
          format: int32
    retryPolicy:
      interval: 30m
      maxAttempts: -1
    events:
      - onFailed
    timeout: 10m
```

**实现：**
```go
func (e *Executor) ExecuteProcessOrder(ctx context.Context, params *taskgen.ProcessOrderParameters) error {
    order, err := e.model.GetOrder(ctx, params.OrderId)
    if err != nil {
        return err
    }
    
    // 处理订单
    if err := e.orderService.ProcessOrder(ctx, order); err != nil {
        // 如果重试用尽，这将触发 OnProcessOrderFailed
        return err
    }
    
    return nil
}

func (e *Executor) OnProcessOrderFailed(ctx context.Context, taskID int32, params *taskgen.ProcessOrderParameters, tx pgx.Tx) error {
    // 钩子直接接收原始参数，具有完全的类型安全性
    log.Error("订单处理永久失败", 
        zap.Int32("taskID", taskID),
        zap.Int32("orderId", params.OrderId))
    
    // 处理失败 - 通知客服、更新订单状态等
    // 使用事务上下文进行额外的数据库操作
    return e.orderService.HandleFailure(ctx, params.OrderId, taskID)
}
```

### 示例 4：带自定义参数的复杂失败处理

**任务定义：**
```yaml
tasks:
  - name: SendNotification
    description: "向用户发送通知"
    parameters:
      type: object
      required: [userId, message]
      properties:
        userId:
          type: integer
          format: int32
        message:
          type: string
        priority:
          type: string
          enum: [low, medium, high]
    retryPolicy:
      interval: 5m
      maxAttempts: -1
    events:
      - onFailed
```

**实现：**
```go
func (e *Executor) ExecuteSendNotification(ctx context.Context, params *taskgen.SendNotificationParameters) error {
    return e.notificationService.Send(ctx, params.UserId, params.Message, params.Priority)
}

func (e *Executor) OnSendNotificationFailed(ctx context.Context, taskID int32, params *taskgen.SendNotificationParameters, tx pgx.Tx) error {
    // 钩子直接接收原始参数，具有完全的类型安全性
    log.Error("通知发送永久失败", 
        zap.Int32("taskID", taskID),
        zap.Int32("userId", params.UserId),
        zap.String("message", params.Message),
        zap.String("priority", params.Priority))
    
    // 使用原始上下文升级到管理员
    return e.adminService.EscalateFailedNotification(ctx, EscalationRequest{
        FailedTaskID: taskID,
        OriginalUserId: params.UserId,
        OriginalMessage: params.Message,
        Priority: params.Priority,
        EscalationLevel: "admin",
    })
}
```

### 真实世界示例：带失败处理的删除操作

这个来自 Anchor 代码库的示例展示了如何实现一个删除敏感数据的任务，并进行适当的失败处理：

**任务定义（api/tasks.yaml）：**
```yaml
tasks:
  - name: deleteOpaqueKey
    description: 删除不透明密钥
    parameters:
      type: object
      required: [keyID]
      properties:
        keyID:
          type: integer
          format: int64
          description: 要删除的不透明密钥的 ID
    retryPolicy:
      interval: 30m
      maxAttempts: -1
    events:
      - onFailed
```

**生成的类型：**
运行 `anchor generate` 后，您得到：
```go
type DeleteOpaqueKeyParameters struct {
    KeyID int64 `json:"keyID"`
}

type ExecutorInterface interface {
    ExecuteDeleteOpaqueKey(ctx context.Context, params *DeleteOpaqueKeyParameters) error
    OnDeleteOpaqueKeyFailed(ctx context.Context, taskID int32, params *DeleteOpaqueKeyParameters, tx pgx.Tx) error
}
```

**实现：**
```go
func (e *Executor) ExecuteDeleteOpaqueKey(ctx context.Context, params *taskgen.DeleteOpaqueKeyParameters) error {
    // 尝试删除不透明密钥
    err := e.model.DeleteOpaqueKey(ctx, params.KeyID)
    if err != nil {
        // 如果删除失败，重试后将触发 OnDeleteOpaqueKeyFailed
        return fmt.Errorf("删除不透明密钥 %d 失败: %w", params.KeyID, err)
    }
    
    log.Info("成功删除不透明密钥", zap.Int64("keyID", params.KeyID))
    return nil
}

func (e *Executor) OnDeleteOpaqueKeyFailed(ctx context.Context, taskID int32, params *taskgen.DeleteOpaqueKeyParameters, tx pgx.Tx) error {
    // 钩子直接接收原始参数，具有完全的类型安全性
    log.Error("严重：所有重试后删除不透明密钥失败", 
        zap.Int64("keyID", params.KeyID),
        zap.Int32("failedTaskID", taskID))
    
    // 通知安全团队密钥删除失败
    // 如果需要，使用事务上下文进行额外的数据库操作
    return e.securityService.NotifyFailedKeyDeletion(ctx, params.KeyID, taskID)
}
```

**启动任务：**
```go
func (h *Handler) DeleteKey(c *fiber.Ctx) error {
    keyID := c.Params("id")
    keyIDInt, err := strconv.ParseInt(keyID, 10, 64)
    if err != nil {
        return c.Status(400).JSON(fiber.Map{"error": "无效的密钥 ID"})
    }
    
    // 排队删除任务
    taskID, err := h.taskRunner.RunDeleteOpaqueKey(c.Context(), &taskgen.DeleteOpaqueKeyParameters{
        KeyID: keyIDInt,
    })
    if err != nil {
        return err
    }
    
    return c.JSON(fiber.Map{
        "message": "密钥删除已排队",
        "taskID": taskID,
    })
}
```

此示例演示了：
- **优雅降级**：如果删除失败，系统不会简单放弃
- **审计跟踪**：失败的删除会被记录和跟踪
- **管理监督**：关键失败会升级到安全团队
- **事务安全**：任务状态更新和钩子执行都是原子的
- **类型安全**：钩子方法接收强类型参数而不是原始 JSON

## 最佳实践

1. **保持任务幂等** - 任务可能会重试，因此确保它们可以安全地多次执行
2. **使用唯一标签** - 防止关键操作的重复任务
3. **设置适当的超时** - 不要让任务无限期运行
4. **优雅地处理错误** - 使用特定的错误类型来控制重试行为
5. **仔细设计失败钩子** - 失败钩子应处理清理、通知或升级
6. **监控任务性能** - 使用指标跟踪任务执行时间和失败率
7. **使用事务** - 在数据库事务中排队任务以保证一致性
8. **测试失败场景** - 确保您的失败钩子正常工作且不会创建无限循环

## 工作者配置

当您启动 Anchor 应用程序时，工作者会自动运行。您可以配置工作者行为：

```go
// 为特定环境禁用工作者
cfg := &config.Config{
    Worker: config.WorkerConfig{
        Disable: true,  // 禁用工作者
    },
}
```

工作者每秒轮询数据库以查找待处理任务，并根据可用的 goroutine 以可配置的并发性处理它们。