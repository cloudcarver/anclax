# Anchor 中的异步任务

[English](asynctask.md) | 中文

本文档提供了 Anchor 异步任务系统的全面概述，涵盖用户体验流程和底层技术机制。

## 目录

- [概述](#概述)
- [用户体验流程](#用户体验流程)
- [底层架构](#底层架构)
- [任务生命周期](#任务生命周期)
- [高级功能](#高级功能)
- [性能和可靠性](#性能和可靠性)

## 概述

Anchor 的异步任务系统提供了一种强大、可靠的方式来执行后台工作，具有至少一次交付保证。该系统围绕一个简单的原则设计：声明式定义任务，在代码中实现它们，让框架处理排队、重试和监控的所有复杂性。

### 主要优势

- **至少一次交付**：任务保证至少成功执行一次
- **自动重试**：失败的任务根据可配置的策略进行重试
- **类型安全**：任务参数的完整编译时类型检查
- **事务支持**：任务可以在数据库事务中排队
- **Cron 调度**：任务可以使用 cron 表达式按计划运行
- **失败钩子**：任务永久失败时的自动清理和通知

## 用户体验流程

### 1. 任务定义阶段

用户首先在 `api/tasks.yaml` 中使用声明式 YAML 格式定义任务：

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

用户运行 `anchor gen` 来生成类型安全的接口：

```bash
anchor gen
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

### 数据库模式

异步任务系统使用几个数据库表：

```sql
-- 核心任务表
CREATE TABLE anchor_tasks (
    id SERIAL PRIMARY KEY,
    spec JSONB NOT NULL,           -- 任务类型和参数
    attributes JSONB NOT NULL,     -- 重试策略、超时等
    status TEXT NOT NULL,          -- pending、running、completed、failed
    started_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    unique_tag TEXT UNIQUE         -- 用于防止重复
);

-- Cron 作业调度
CREATE TABLE anchor_cron_jobs (
    id SERIAL PRIMARY KEY,
    task_id INTEGER REFERENCES anchor_tasks(id),
    cron_expression TEXT NOT NULL,
    next_run TIMESTAMP NOT NULL,
    enabled BOOLEAN DEFAULT true
);
```

### 工作者架构

工作者系统由几个组件组成：

#### 1. 任务存储接口
```go
type TaskStoreInterface interface {
    PushTask(ctx context.Context, task *apigen.Task) (int32, error)
    PullTask(ctx context.Context) (*apigen.Task, error)
    UpdateTaskStatus(ctx context.Context, taskID int32, status string) error
    // ... 其他方法
}
```

#### 2. 工作者池
- 工作者作为主应用程序进程中的 goroutine 运行
- 每个工作者每秒轮询待处理任务
- 基于可用系统资源的可配置并发性
- 优雅关闭处理

#### 3. 任务执行流程
```
1. 工作者调用 PullTask() 获取下一个待处理任务
2. 任务状态更新为 "running"
3. 工作者反序列化任务参数
4. 工作者调用适当的执行器方法
5. 成功时：状态更新为 "completed"
6. 失败时：重试逻辑启动或触发失败钩子
```

### 重试机制

重试系统实现了带抖动的指数退避：

```go
type RetryPolicy struct {
    Interval    string `json:"interval"`    // "5m" 或 "1m,2m,4m,8m"
    MaxAttempts int    `json:"maxAttempts"` // -1 表示无限
}
```

**重试算法：**
1. 解析间隔字符串（简单持续时间或逗号分隔列表）
2. 根据尝试次数计算下次重试时间
3. 添加抖动以防止雷群效应
4. 用下次执行时间更新任务
5. 重试时间到达时工作者接收任务

### 事务安全

任务可以在数据库事务中排队：

```go
func (s *Service) CreateUserWithWelcomeEmail(ctx context.Context, userData UserData) error {
    return s.model.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
        // 创建用户
        user, err := s.model.CreateUser(ctx, userData)
        if err != nil {
            return err
        }
        
        // 在同一事务中排队欢迎邮件
        _, err = s.taskRunner.RunSendWelcomeEmailWithTx(ctx, tx, &taskgen.SendWelcomeEmailParameters{
            UserId:     user.ID,
            TemplateId: "welcome",
        })
        
        return err
    })
}
```

**事务保证：**
- 如果用户创建失败，欢迎邮件任务不会排队
- 如果任务排队失败，用户创建会回滚
- 两个操作要么都成功要么都失败，原子性

## 任务生命周期

### 状态转换

```
pending → running → completed
    ↓         ↓
    ↓    → failed → pending (重试)
    ↓              ↓
    ↓         → failed (永久)
    ↓              ↓
    ↓         → 钩子执行
    ↓
    → cancelled (手动干预)
```

### 详细生命周期

1. **任务创建**
   - 任务定义验证
   - 参数序列化
   - 数据库记录创建，状态为 `pending`
   - 检查唯一标签（如果提供）

2. **任务接收**
   - 工作者查询最旧的待处理任务
   - 任务状态更新为 `running`
   - 工作者进程开始执行

3. **任务执行**
   - 参数反序列化和验证
   - 使用上下文和事务调用执行器方法
   - 根据超时监控执行时间

4. **成功路径**
   - 任务状态更新为 `completed`
   - 指标更新
   - 任务从活动处理中移除

5. **失败路径**
   - 错误记录和分类
   - 查询重试策略
   - 如果还有重试：状态 → `pending`，更新 next_run
   - 如果重试用尽：状态 → `failed`，触发失败钩子

6. **失败钩子执行**
   - 在事务中调用钩子方法
   - 原始任务参数传递给钩子
   - 钩子成功/失败影响最终任务状态

### Cron 作业生命周期

定时任务遵循不同的生命周期：

1. **Cron 作业注册**
   - Cron 表达式解析和验证
   - 计算下次执行时间
   - 作业在调度器中注册

2. **定时执行**
   - 当 next_run 时间到达时，创建新任务实例
   - 任务遵循正常执行生命周期
   - 重新计算下次执行时间

3. **Cron 作业管理**
   - 作业可以暂停/恢复
   - Cron 表达式可以更新
   - 作业可以删除

## 高级功能

### 任务覆盖

任务行为的运行时自定义：

```go
taskID, err := taskRunner.RunSendWelcomeEmail(ctx, params,
    taskcore.WithRetryPolicy("1h", 5),           // 自定义重试
    taskcore.WithTimeout("2m"),                  // 自定义超时
    taskcore.WithUniqueTag("user-123-welcome"),  // 防止重复
    taskcore.WithDelay(time.Hour),               // 延迟执行
)
```

**覆盖实现：**
- 覆盖作为函数选项应用
- 它们在数据库插入前修改任务属性
- 类型安全验证确保覆盖兼容性

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
- 钩子仅在永久失败时触发
- 钩子接收原始任务参数，具有完全的类型安全性
- 钩子在与状态更新相同的事务中执行
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