# Anchor 中的事务管理

Anchor 被设计为一个**单一内聚的终极后端框架**，建立在一个核心原则之上：**`WithTx` 模式**。每个与数据库交互的组件都提供独立方法和接受 `pgx.Tx` 的事务变体，实现在单个事务内无缝组合操作。

本文档解释了 Anchor 的事务系统如何工作，重点关注 `WithTx` 模式如何使插件系统、任务执行、钩子和服务方法协同工作。

## 核心原则：`WithTx` 模式

Anchor 的架构建立在一个基本原则之上：**每个数据库操作都应该有独立和事务两种形式**：

- **独立方法**：处理自己的事务生命周期
- **`WithTx` 方法**：接受现有事务并参与其中

这种模式确保：

1. **可组合性**：操作可以组合成更大的事务
2. **原子性**：复杂工作流要么完全完成，要么回滚
3. **一致性**：数据库约束在所有操作中都得到维护
4. **内聚性**：所有框架组件都遵循相同的事务模式

## 事务传递机制

### 核心模式：`pgx.Tx` 传播

Anchor 使用一致的模式在函数边界间传播 PostgreSQL 事务（`pgx.Tx`）：

```go
// 基本模式：函数同时接受上下文和事务
func SomeOperation(ctx context.Context, tx pgx.Tx, params SomeParams) error {
    // 所有数据库操作使用提供的事务
    return someModel.WithTx(tx).DoSomething(ctx, params)
}
```

### 模型接口事务支持

`ModelInterface` 提供两个关键的事务管理方法：

```go
type ModelInterface interface {
    // 启动新事务并提供 tx 和 model
    RunTransactionWithTx(ctx context.Context, f func(tx pgx.Tx, model ModelInterface) error) error
    
    // 创建绑定到现有事务的新模型实例
    SpawnWithTx(tx pgx.Tx) ModelInterface
}
```

**实现细节：**

```go
func (m *Model) RunTransactionWithTx(ctx context.Context, f func(tx pgx.Tx, model ModelInterface) error) error {
    tx, err := m.beginTx(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx) // 如果提交未发生，总是回滚
    
    txm := m.SpawnWithTx(tx) // 创建事务绑定的模型
    
    if err := f(tx, txm); err != nil {
        return err // 回滚在 defer 中发生
    }
    
    return tx.Commit(ctx) // 只有在没有错误时才提交
}
```

## 插件系统架构

### 插件接口

Anchor 中的插件实现一个简单的接口，允许它们与系统的不同部分集成：

```go
type Plugin struct {
    serverInterface apigen.ServerInterface
    validator       apigen.Validator
    taskHandler     worker.TaskHandler
}

func (p *Plugin) Plug(anchorApp *anchor_app.Application) {
    p.PlugToFiberApp(anchorApp.GetServer().GetApp())
    p.PlugToWorker(anchorApp.GetWorker())
}
```

### 事务感知组件

所有与数据库交互的插件组件都遵循 `WithTx` 模式：

1. **任务处理器**：为所有操作接收 `pgx.Tx`
2. **钩子**：在触发事件的同一事务内执行
3. **生命周期处理器**：事务性地管理任务状态变化
4. **服务方法**：提供独立和 `WithTx` 两种变体

## `WithTx` 模式实践

### 跨组件的通用应用

Anchor 中每个执行数据库操作的组件都遵循 `WithTx` 模式：

#### 1. 模型层
```go
type ModelInterface interface {
    // 独立：管理自己的事务
    CreateUser(ctx context.Context, username string) (*User, error)
    
    // WithTx：参与现有事务
    SpawnWithTx(tx pgx.Tx) ModelInterface
}
```

#### 2. 服务层
```go
type ServiceInterface interface {
    // 独立：创建和管理事务
    CreateNewUser(ctx context.Context, username, password string) (int32, error)
    
    // WithTx：使用提供的事务
    CreateNewUserWithTx(ctx context.Context, tx pgx.Tx, username, password string) (int32, error)
}
```

#### 3. 任务系统
```go
type TaskRunner interface {
    // 独立：为任务创建创建自己的事务
    RunTask(ctx context.Context, params *TaskParams) (int32, error)
    
    // WithTx：在现有事务内创建任务
    RunTaskWithTx(ctx context.Context, tx pgx.Tx, params *TaskParams) (int32, error)
}
```

#### 4. 存储组件
```go
type TaskStoreInterface interface {
    // 独立操作
    PushTask(ctx context.Context, task *apigen.Task) (int32, error)
    
    // WithTx：在现有事务内操作
    WithTx(tx pgx.Tx) TaskStoreInterface
}
```

### 服务方法：复杂业务逻辑的事务化

Anchor 的服务层通过为所有业务操作提供事务变体，展示了 `WithTx` 模式的威力：

#### 示例：用户创建服务

```go
// 独立方法 - 管理自己的事务
func (s *Service) CreateNewUser(ctx context.Context, username, password string) (int32, error) {
    var userID int32
    if err := s.m.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
        // 委托给事务变体
        id, err := s.CreateNewUserWithTx(ctx, tx, username, password)
        userID = id
        return err
    }); err != nil {
        return 0, err
    }
    return userID, nil
}

// WithTx 方法 - 参与现有事务
func (s *Service) CreateNewUserWithTx(ctx context.Context, tx pgx.Tx, username, password string) (int32, error) {
    // 生成密码哈希
    salt, hash, err := s.generateSaltAndHash(password)
    if err != nil {
        return 0, err
    }
    
    // 使用事务绑定的模型
    txm := s.m.SpawnWithTx(tx)
    
    // 创建组织
    org, err := txm.CreateOrg(ctx, fmt.Sprintf("%s's Org", username))
    if err != nil {
        return 0, err
    }
    
    // 在同一事务内执行钩子
    if err := s.hooks.OnOrgCreated(ctx, tx, org.ID); err != nil {
        return 0, err
    }
    
    // 创建用户
    user, err := txm.CreateUser(ctx, querier.CreateUserParams{
        Username:     username,
        PasswordHash: hash,
        PasswordSalt: salt,
        OrgID:        org.ID,
    })
    if err != nil {
        return 0, err
    }
    
    // 执行用户创建钩子
    if err := s.hooks.OnUserCreated(ctx, tx, user.ID); err != nil {
        return 0, err
    }
    
    return user.ID, nil
}
```

### 可组合性：终极威力

`WithTx` 模式实现跨不同层的操作无缝组合：

```go
func (s *SomeService) ComplexBusinessOperation(ctx context.Context, params BusinessParams) error {
    return s.model.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
        // 1. 创建用户（服务层）
        userID, err := s.authService.CreateNewUserWithTx(ctx, tx, params.Username, params.Password)
        if err != nil {
            return err
        }
        
        // 2. 调度后台任务（任务系统）
        taskID, err := s.taskRunner.RunWelcomeEmailWithTx(ctx, tx, &WelcomeEmailParams{
            UserID: userID,
        })
        if err != nil {
            return err
        }
        
        // 3. 创建相关资源（模型层）
        txModel := s.model.SpawnWithTx(tx)
        if err := txModel.CreateUserProfile(ctx, userID); err != nil {
            return err
        }
        
        // 4. 记录审计事件（另一个服务）
        return s.auditService.LogEventWithTx(ctx, tx, "user_created", userID)
    })
}
```

**关键优势：**
- 如果任何步骤失败，整个操作回滚
- 不会提交部分状态更改
- 所有组件参与同一事务
- 钩子在事务上下文内执行

## 任务运行器和执行器：至少一次交付

### 任务运行器架构

任务运行器提供事务和非事务接口：

```go
type TaskRunner interface {
    // 非事务：启动自己的事务
    RunTask(ctx context.Context, params *TaskParams) (int32, error)
    
    // 事务：使用提供的事务
    RunTaskWithTx(ctx context.Context, tx pgx.Tx, params *TaskParams) (int32, error)
}
```

### 至少一次交付保证

至少一次交付保证通过几种机制实现：

#### 1. 事务性任务创建

```go
func (c *Client) RunTaskWithTx(ctx context.Context, tx pgx.Tx, params *TaskParams, overrides ...taskcore.TaskOverride) (int32, error) {
    // 任务在与调用操作相同的事务内创建
    return c.runTask(ctx, c.taskStore.WithTx(tx), params, overrides...)
}
```

**关键点：**
- 任务在与业务逻辑相同的事务内插入数据库
- 如果事务失败，任务不会被创建
- 如果事务成功，任务保证存在并将被处理

#### 2. 工作器轮询和执行

```go
func (w *Worker) pullAndRun(parentCtx context.Context) error {
    return w.model.RunTransactionWithTx(parentCtx, func(tx pgx.Tx, txm model.ModelInterface) error {
        // 1. 拉取任务（带行级锁定）
        qtask, err := txm.PullTask(parentCtx)
        if err != nil {
            return err
        }
        
        // 2. 在同一事务内执行任务
        return w.runTaskWithTx(parentCtx, tx, task)
    })
}
```

**保证机制：**
- 任务通过数据库级锁定拉取（防止重复处理）
- 任务执行在与拉取相同的事务内发生
- 如果执行失败，事务回滚，任务保持可用
- 任务状态只在成功完成时更新

#### 3. 任务状态管理

```go
func (w *Worker) runTaskWithTx(ctx context.Context, tx pgx.Tx, task apigen.Task) error {
    txm := w.model.SpawnWithTx(tx)
    
    // 增加尝试计数器（即使任务失败也会提交）
    if err := txm.IncrementAttempts(ctx, task.ID); err != nil {
        return err
    }
    
    // 执行实际任务
    err = w.taskHandler.HandleTask(ctx, tx, &task.Spec)
    if err != nil {
        // 处理失败（重试逻辑，错误记录）
        return w.lifeCycleHandler.HandleFailed(ctx, tx, task, err)
    } else {
        // 处理成功（标记完成，运行钩子）
        return w.lifeCycleHandler.HandleCompleted(ctx, tx, task)
    }
}
```

### 示例：计数器增量任务

这是一个完整示例，展示任务执行器如何接收和使用事务：

```go
type Executor struct {
    model model.ModelInterface
}

func (e *Executor) ExecuteIncrementCounter(ctx context.Context, tx pgx.Tx, params *IncrementCounterParameters) error {
    // 为所有数据库操作使用事务绑定的模型
    txModel := e.model.SpawnWithTx(tx)
    
    // 所有操作都是同一事务的一部分
    return txModel.IncrementCounter(ctx)
}
```

**事务流程：**
1. 工作器在事务 T1 内拉取任务
2. 工作器用 T1 调用 `ExecuteIncrementCounter`
3. 执行器使用 T1 执行数据库操作
4. 如果执行器成功，T1 提交（任务标记完成）
5. 如果执行器失败，T1 回滚（任务保持待处理以重试）

## 钩子系统：保证执行

### 钩子类型

Anchor 提供两种类型的钩子：

1. **事务钩子**：在同一事务内执行
2. **异步钩子**：通过任务系统异步执行

```go
type AnchorHookInterface interface {
    // 事务钩子 - 在同一 tx 内执行
    OnUserCreated(ctx context.Context, tx pgx.Tx, userID int32) error
    
    // 异步钩子 - 在事务外执行
    OnCreateToken(ctx context.Context, userID int32, macaroon *macaroons.Macaroon) error
}
```

### 事务钩子执行

```go
func (b *BaseHook) OnUserCreated(ctx context.Context, tx pgx.Tx, userID int32) error {
    // 所有注册的钩子在同一事务内执行
    for _, hook := range b.OnUserCreatedHooks {
        if err := hook(ctx, tx, userID); err != nil {
            return err // 事务将被回滚
        }
    }
    return nil
}
```

### 钩子保证

#### 1. 原子性保证

```go
func (s *Service) CreateUser(ctx context.Context, username, password string) error {
    return s.model.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
        // 1. 创建用户
        userID, err := txm.CreateUser(ctx, username, password)
        if err != nil {
            return err
        }
        
        // 2. 在同一事务内执行钩子
        if err := s.hooks.OnUserCreated(ctx, tx, userID); err != nil {
            return err // 也会回滚用户创建
        }
        
        return nil // 用户创建和钩子一起提交
    })
}
```

#### 2. 失败处理

如果任何钩子失败：
- 整个事务（包括原始操作）被回滚
- 不会提交部分状态更改
- 系统保持一致状态

### 任务生命周期钩子

任务系统也提供保证执行的生命周期钩子：

```go
type TaskHandler interface {
    HandleTask(ctx context.Context, tx pgx.Tx, spec TaskSpec) error
    OnTaskFailed(ctx context.Context, tx pgx.Tx, failedTaskSpec TaskSpec, taskID int32) error
}
```

**示例实现：**

```go
func (f *TaskHandler) OnTaskFailed(ctx context.Context, tx pgx.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
    // 这个钩子保证在任务失败时执行
    // 它在与失败处理相同的事务内运行
    return f.executor.OnTaskFailed(ctx, taskID, failedTaskSpec, tx)
}
```

## 重试和错误处理

### 重试策略

任务可以配置重试策略：

```go
attributes.RetryPolicy = &apigen.TaskRetryPolicy{
    Interval:    "30s",      // 重试间隔等待 30 秒
    MaxAttempts: 3,          // 最多尝试 3 次
}
```

### 重试机制

```go
func (a *TaskLifeCycleHandler) HandleFailed(ctx context.Context, tx pgx.Tx, task apigen.Task, err error) error {
    if task.Attributes.RetryPolicy != nil {
        if task.Attempts < task.Attributes.RetryPolicy.MaxAttempts {
            // 通过更新 started_at 时间调度重试
            interval, _ := time.ParseDuration(task.Attributes.RetryPolicy.Interval)
            nextTime := time.Now().Add(interval)
            
            return txm.UpdateTaskStartedAt(ctx, UpdateTaskStartedAtParams{
                ID:        task.ID,
                StartedAt: &nextTime,
            })
        }
    }
    
    // 达到最大尝试次数 - 标记为失败
    return txm.UpdateTaskStatus(ctx, UpdateTaskStatusParams{
        ID:     task.ID,
        Status: string(apigen.Failed),
    })
}
```

## `WithTx` 模式最佳实践

### 1. 总是提供两种变体

设计新组件时，总是提供独立和 `WithTx` 两种变体：

```go
// ✅ 好：提供两种变体
type MyService interface {
    ProcessOrder(ctx context.Context, orderID int32) error
    ProcessOrderWithTx(ctx context.Context, tx pgx.Tx, orderID int32) error
}

// 实现模式
func (s *MyService) ProcessOrder(ctx context.Context, orderID int32) error {
    return s.model.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
        return s.ProcessOrderWithTx(ctx, tx, orderID)
    })
}

func (s *MyService) ProcessOrderWithTx(ctx context.Context, tx pgx.Tx, orderID int32) error {
    // 使用事务绑定组件的实际实现
    txm := s.model.SpawnWithTx(tx)
    // ... 业务逻辑
}
```

### 2. 总是使用提供的事务

```go
// ✅ 好：使用提供的事务
func (e *Executor) ExecuteTask(ctx context.Context, tx pgx.Tx, params *Params) error {
    return e.model.SpawnWithTx(tx).DoWork(ctx, params)
}

// ❌ 坏：启动新事务
func (e *Executor) ExecuteTask(ctx context.Context, tx pgx.Tx, params *Params) error {
    return e.model.RunTransaction(ctx, func(model ModelInterface) error {
        return model.DoWork(ctx, params)
    })
}
```

### 3. 在事务上下文中优先使用 `WithTx` 方法

当你已经在事务内时，总是使用其他服务的 `WithTx` 变体：

```go
// ✅ 好：在事务内使用 WithTx 方法
func (s *OrderService) ProcessOrderWithTx(ctx context.Context, tx pgx.Tx, orderID int32) error {
    // 使用其他服务的 WithTx 变体
    userID, err := s.userService.GetUserByOrderWithTx(ctx, tx, orderID)
    if err != nil {
        return err
    }
    
    // 在同一事务内调度通知任务
    _, err = s.taskRunner.RunOrderNotificationWithTx(ctx, tx, &NotificationParams{
        UserID:  userID,
        OrderID: orderID,
    })
    return err
}

// ❌ 坏：在现有事务内创建新事务
func (s *OrderService) ProcessOrderWithTx(ctx context.Context, tx pgx.Tx, orderID int32) error {
    // 这会创建一个单独的事务！
    userID, err := s.userService.GetUserByOrder(ctx, orderID)
    if err != nil {
        return err
    }
    
    // 这也会创建一个单独的事务！
    _, err = s.taskRunner.RunOrderNotification(ctx, &NotificationParams{
        UserID:  userID,
        OrderID: orderID,
    })
    return err
}
```

### 4. 适当处理错误

```go
func (e *Executor) ExecuteTask(ctx context.Context, tx pgx.Tx, params *Params) error {
    if err := e.validateParams(params); err != nil {
        // 致命错误 - 不重试
        return taskcore.ErrFatalTask
    }
    
    if err := e.doWork(ctx, tx, params); err != nil {
        if isTemporaryError(err) {
            // 可重试错误
            return err
        }
        // 致命错误
        return taskcore.ErrFatalTask
    }
    
    return nil
}
```

### 5. 设计幂等操作

由于任务保证至少执行一次，设计你的任务执行器为幂等的：

```go
func (e *Executor) ExecuteProcessPayment(ctx context.Context, tx pgx.Tx, params *PaymentParams) error {
    txModel := e.model.SpawnWithTx(tx)
    
    // 检查是否已处理（幂等性）
    payment, err := txModel.GetPayment(ctx, params.PaymentID)
    if err != nil {
        return err
    }
    
    if payment.Status == "processed" {
        return nil // 已处理，安全返回成功
    }
    
    // 处理支付...
    return txModel.UpdatePaymentStatus(ctx, params.PaymentID, "processed")
}
```

## 结论：`WithTx` 模式作为基础

Anchor 通过普遍应用 `WithTx` 模式实现成为**单一内聚的终极后端框架**的目标。这个核心原则提供：

### 框架级一致性
- **每个组件**都遵循相同的事务模式
- **每个数据库操作**都有独立和事务两种变体
- **每个层**（模型、服务、任务、存储）都说相同的事务语言

### 强大保证
1. **事务可组合性**：任何操作都可以与任何其他操作在单个事务中组合
2. **至少一次交付**：通过事务创建和原子执行保证任务被执行
3. **钩子保证**：所有钩子在与触发操作相同的事务内执行
4. **数据一致性**：复杂业务工作流在所有组件中维护 ACID 属性

### 开发者体验
- **可预测的 API**：如果方法存在，其 `WithTx` 变体也存在
- **无缝组合**：来自不同层的操作可以轻松组合
- **故障安全设计**：部分失败永远不会让系统处于不一致状态
- **插件兼容性**：所有插件自动继承事务能力

`WithTx` 模式将可能是分离组件集合的东西转变为真正内聚的框架，其中每个部分都事务性地协同工作。这种设计使开发者能够构建复杂、可靠的后端系统，并确信数据一致性在每个级别都得到维护。

**本质上，`WithTx` 不仅仅是方法命名约定——它是使 Anchor 成为终极后端框架的架构基础。**