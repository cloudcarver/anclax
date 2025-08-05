# Transaction Management in Anchor

Anchor is designed as a **single cohesive ultimate backend framework** built around one core principle: **the `WithTx` pattern**. Every component that interacts with the database provides both standalone methods and transactional variants that accept `pgx.Tx`, enabling seamless composition of operations within a single transaction.

This document explains how Anchor's transaction system works, focusing on how the `WithTx` pattern enables the plugin system, task execution, hooks, and service methods to work together cohesively.

## Core Principle: The `WithTx` Pattern

Anchor's architecture is built on the fundamental principle that **every database operation should be available in both standalone and transactional forms**:

- **Standalone methods**: Handle their own transaction lifecycle
- **`WithTx` methods**: Accept an existing transaction and participate in it

This pattern ensures:

1. **Composability**: Operations can be combined into larger transactions
2. **Atomicity**: Complex workflows either complete entirely or are rolled back
3. **Consistency**: Database constraints are maintained across all operations
4. **Cohesiveness**: All framework components follow the same transaction pattern

## Transaction Delivery Mechanism

### Core Pattern: `pgx.Tx` Propagation

Anchor uses a consistent pattern to propagate PostgreSQL transactions (`pgx.Tx`) across function boundaries:

```go
// Base pattern: Functions accept both context and transaction
func SomeOperation(ctx context.Context, tx pgx.Tx, params SomeParams) error {
    // All database operations use the provided transaction
    return someModel.WithTx(tx).DoSomething(ctx, params)
}
```

### Model Interface Transaction Support

The `ModelInterface` provides two key methods for transaction management:

```go
type ModelInterface interface {
    // Starts a new transaction and provides both tx and model
    RunTransactionWithTx(ctx context.Context, f func(tx pgx.Tx, model ModelInterface) error) error
    
    // Creates a new model instance bound to an existing transaction
    SpawnWithTx(tx pgx.Tx) ModelInterface
}
```

**Implementation details:**

```go
func (m *Model) RunTransactionWithTx(ctx context.Context, f func(tx pgx.Tx, model ModelInterface) error) error {
    tx, err := m.beginTx(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx) // Always rollback if commit doesn't happen
    
    txm := m.SpawnWithTx(tx) // Create transaction-bound model
    
    if err := f(tx, txm); err != nil {
        return err // Rollback happens in defer
    }
    
    return tx.Commit(ctx) // Only commit if no errors
}
```

## Plugin System Architecture

### Plugin Interface

Plugins in Anchor implement a simple interface that allows them to integrate with different parts of the system:

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

### Transaction-Aware Components

All plugin components that interact with the database follow the `WithTx` pattern:

1. **Task Handlers**: Receive `pgx.Tx` for all operations
2. **Hooks**: Execute within the same transaction as the triggering event
3. **Lifecycle Handlers**: Manage task state changes transactionally
4. **Service Methods**: Provide both standalone and `WithTx` variants

## The `WithTx` Pattern in Practice

### Universal Application Across Components

Every component in Anchor that performs database operations follows the `WithTx` pattern:

#### 1. Model Layer
```go
type ModelInterface interface {
    // Standalone: manages its own transaction
    CreateUser(ctx context.Context, username string) (*User, error)
    
    // WithTx: participates in existing transaction
    SpawnWithTx(tx pgx.Tx) ModelInterface
}
```

#### 2. Service Layer
```go
type ServiceInterface interface {
    // Standalone: creates and manages transaction
    CreateNewUser(ctx context.Context, username, password string) (int32, error)
    
    // WithTx: uses provided transaction
    CreateNewUserWithTx(ctx context.Context, tx pgx.Tx, username, password string) (int32, error)
}
```

#### 3. Task System
```go
type TaskRunner interface {
    // Standalone: creates its own transaction for task creation
    RunTask(ctx context.Context, params *TaskParams) (int32, error)
    
    // WithTx: creates task within existing transaction
    RunTaskWithTx(ctx context.Context, tx pgx.Tx, params *TaskParams) (int32, error)
}
```

#### 4. Storage Components
```go
type TaskStoreInterface interface {
    // Standalone operations
    PushTask(ctx context.Context, task *apigen.Task) (int32, error)
    
    // WithTx: operates within existing transaction
    WithTx(tx pgx.Tx) TaskStoreInterface
}
```

### Service Methods: Complex Business Logic Made Transactional

Anchor's service layer demonstrates the power of the `WithTx` pattern by providing transactional variants of all business operations:

#### Example: User Creation Service

```go
// Standalone method - manages its own transaction
func (s *Service) CreateNewUser(ctx context.Context, username, password string) (int32, error) {
    var userID int32
    if err := s.m.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
        // Delegate to the transactional variant
        id, err := s.CreateNewUserWithTx(ctx, tx, username, password)
        userID = id
        return err
    }); err != nil {
        return 0, err
    }
    return userID, nil
}

// WithTx method - participates in existing transaction
func (s *Service) CreateNewUserWithTx(ctx context.Context, tx pgx.Tx, username, password string) (int32, error) {
    // Generate password hash
    salt, hash, err := s.generateSaltAndHash(password)
    if err != nil {
        return 0, err
    }
    
    // Use transaction-bound model
    txm := s.m.SpawnWithTx(tx)
    
    // Create organization
    org, err := txm.CreateOrg(ctx, fmt.Sprintf("%s's Org", username))
    if err != nil {
        return 0, err
    }
    
    // Execute hooks within the same transaction
    if err := s.hooks.OnOrgCreated(ctx, tx, org.ID); err != nil {
        return 0, err
    }
    
    // Create user
    user, err := txm.CreateUser(ctx, querier.CreateUserParams{
        Username:     username,
        PasswordHash: hash,
        PasswordSalt: salt,
        OrgID:        org.ID,
    })
    if err != nil {
        return 0, err
    }
    
    // Execute user creation hooks
    if err := s.hooks.OnUserCreated(ctx, tx, user.ID); err != nil {
        return 0, err
    }
    
    return user.ID, nil
}
```

### Composability: The Ultimate Power

The `WithTx` pattern enables seamless composition of operations across different layers:

```go
func (s *SomeService) ComplexBusinessOperation(ctx context.Context, params BusinessParams) error {
    return s.model.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
        // 1. Create user (service layer)
        userID, err := s.authService.CreateNewUserWithTx(ctx, tx, params.Username, params.Password)
        if err != nil {
            return err
        }
        
        // 2. Schedule background task (task system)
        taskID, err := s.taskRunner.RunWelcomeEmailWithTx(ctx, tx, &WelcomeEmailParams{
            UserID: userID,
        })
        if err != nil {
            return err
        }
        
        // 3. Create related resources (model layer)
        txModel := s.model.SpawnWithTx(tx)
        if err := txModel.CreateUserProfile(ctx, userID); err != nil {
            return err
        }
        
        // 4. Log audit event (another service)
        return s.auditService.LogEventWithTx(ctx, tx, "user_created", userID)
    })
}
```

**Key benefits:**
- If any step fails, the entire operation rolls back
- No partial state changes are committed
- All components participate in the same transaction
- Hooks execute within the transaction context

## Task Runner and Executor: At-Least-Once Delivery

### Task Runner Architecture

The task runner provides both transactional and non-transactional interfaces:

```go
type TaskRunner interface {
    // Non-transactional: starts its own transaction
    RunTask(ctx context.Context, params *TaskParams) (int32, error)
    
    // Transactional: uses provided transaction
    RunTaskWithTx(ctx context.Context, tx pgx.Tx, params *TaskParams) (int32, error)
}
```

### At-Least-Once Delivery Guarantee

The at-least-once delivery guarantee is implemented through several mechanisms:

#### 1. Transactional Task Creation

```go
func (c *Client) RunTaskWithTx(ctx context.Context, tx pgx.Tx, params *TaskParams, overrides ...taskcore.TaskOverride) (int32, error) {
    // Task is created within the same transaction as the calling operation
    return c.runTask(ctx, c.taskStore.WithTx(tx), params, overrides...)
}
```

**Key points:**
- Tasks are inserted into the database within the same transaction as the business logic
- If the transaction fails, the task is not created
- If the transaction succeeds, the task is guaranteed to exist and will be processed

#### 2. Worker Polling and Execution

```go
func (w *Worker) pullAndRun(parentCtx context.Context) error {
    return w.model.RunTransactionWithTx(parentCtx, func(tx pgx.Tx, txm model.ModelInterface) error {
        // 1. Pull task (with row-level locking)
        qtask, err := txm.PullTask(parentCtx)
        if err != nil {
            return err
        }
        
        // 2. Execute task within the same transaction
        return w.runTaskWithTx(parentCtx, tx, task)
    })
}
```

**Guarantee mechanism:**
- Tasks are pulled with database-level locking (preventing duplicate processing)
- Task execution happens within the same transaction as the pull
- If execution fails, the transaction rolls back and the task remains available
- Task status is only updated upon successful completion

#### 3. Task State Management

```go
func (w *Worker) runTaskWithTx(ctx context.Context, tx pgx.Tx, task apigen.Task) error {
    txm := w.model.SpawnWithTx(tx)
    
    // Increment attempts counter (committed even if task fails)
    if err := txm.IncrementAttempts(ctx, task.ID); err != nil {
        return err
    }
    
    // Execute the actual task
    err = w.taskHandler.HandleTask(ctx, tx, &task.Spec)
    if err != nil {
        // Handle failure (retry logic, error logging)
        return w.lifeCycleHandler.HandleFailed(ctx, tx, task, err)
    } else {
        // Handle success (mark completed, run hooks)
        return w.lifeCycleHandler.HandleCompleted(ctx, tx, task)
    }
}
```

### Example: Counter Increment Task

Here's a complete example showing how a task executor receives and uses transactions:

```go
type Executor struct {
    model model.ModelInterface
}

func (e *Executor) ExecuteIncrementCounter(ctx context.Context, tx pgx.Tx, params *IncrementCounterParameters) error {
    // Use the transaction-bound model for all database operations
    txModel := e.model.SpawnWithTx(tx)
    
    // All operations are part of the same transaction
    return txModel.IncrementCounter(ctx)
}
```

**Transaction flow:**
1. Worker pulls task within transaction T1
2. Worker calls `ExecuteIncrementCounter` with T1
3. Executor performs database operations using T1
4. If executor succeeds, T1 commits (task marked complete)
5. If executor fails, T1 rolls back (task remains pending for retry)

## Hook System: Guaranteed Execution

### Hook Types

Anchor provides two types of hooks:

1. **Transactional Hooks**: Execute within the same transaction
2. **Async Hooks**: Execute asynchronously via the task system

```go
type AnchorHookInterface interface {
    // Transactional hook - executes within the same tx
    OnUserCreated(ctx context.Context, tx pgx.Tx, userID int32) error
    
    // Async hook - executes outside the transaction
    OnCreateToken(ctx context.Context, userID int32, macaroon *macaroons.Macaroon) error
}
```

### Transactional Hook Execution

```go
func (b *BaseHook) OnUserCreated(ctx context.Context, tx pgx.Tx, userID int32) error {
    // All registered hooks execute within the same transaction
    for _, hook := range b.OnUserCreatedHooks {
        if err := hook(ctx, tx, userID); err != nil {
            return err // Transaction will be rolled back
        }
    }
    return nil
}
```

### Hook Guarantees

#### 1. Atomicity Guarantee

```go
func (s *Service) CreateUser(ctx context.Context, username, password string) error {
    return s.model.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
        // 1. Create user
        userID, err := txm.CreateUser(ctx, username, password)
        if err != nil {
            return err
        }
        
        // 2. Execute hooks within the same transaction
        if err := s.hooks.OnUserCreated(ctx, tx, userID); err != nil {
            return err // Will rollback user creation too
        }
        
        return nil // Both user creation and hooks committed together
    })
}
```

#### 2. Failure Handling

If any hook fails:
- The entire transaction (including the original operation) is rolled back
- No partial state changes are committed
- The system remains in a consistent state

### Task Lifecycle Hooks

The task system also provides lifecycle hooks that are guaranteed to execute:

```go
type TaskHandler interface {
    HandleTask(ctx context.Context, tx pgx.Tx, spec TaskSpec) error
    OnTaskFailed(ctx context.Context, tx pgx.Tx, failedTaskSpec TaskSpec, taskID int32) error
}
```

**Example implementation:**

```go
func (f *TaskHandler) OnTaskFailed(ctx context.Context, tx pgx.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
    // This hook is guaranteed to execute when a task fails
    // It runs within the same transaction as the failure handling
    return f.executor.OnTaskFailed(ctx, taskID, failedTaskSpec, tx)
}
```

## Retry and Error Handling

### Retry Policy

Tasks can be configured with retry policies:

```go
attributes.RetryPolicy = &apigen.TaskRetryPolicy{
    Interval:    "30s",      // Wait 30 seconds between retries
    MaxAttempts: 3,          // Try up to 3 times
}
```

### Retry Mechanism

```go
func (a *TaskLifeCycleHandler) HandleFailed(ctx context.Context, tx pgx.Tx, task apigen.Task, err error) error {
    if task.Attributes.RetryPolicy != nil {
        if task.Attempts < task.Attributes.RetryPolicy.MaxAttempts {
            // Schedule retry by updating started_at time
            interval, _ := time.ParseDuration(task.Attributes.RetryPolicy.Interval)
            nextTime := time.Now().Add(interval)
            
            return txm.UpdateTaskStartedAt(ctx, UpdateTaskStartedAtParams{
                ID:        task.ID,
                StartedAt: &nextTime,
            })
        }
    }
    
    // Max attempts reached - mark as failed
    return txm.UpdateTaskStatus(ctx, UpdateTaskStatusParams{
        ID:     task.ID,
        Status: string(apigen.Failed),
    })
}
```

## Best Practices for the `WithTx` Pattern

### 1. Always Provide Both Variants

When designing new components, always provide both standalone and `WithTx` variants:

```go
// ✅ Good: Both variants provided
type MyService interface {
    ProcessOrder(ctx context.Context, orderID int32) error
    ProcessOrderWithTx(ctx context.Context, tx pgx.Tx, orderID int32) error
}

// Implementation pattern
func (s *MyService) ProcessOrder(ctx context.Context, orderID int32) error {
    return s.model.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
        return s.ProcessOrderWithTx(ctx, tx, orderID)
    })
}

func (s *MyService) ProcessOrderWithTx(ctx context.Context, tx pgx.Tx, orderID int32) error {
    // Actual implementation using transaction-bound components
    txm := s.model.SpawnWithTx(tx)
    // ... business logic
}
```

### 2. Always Use Provided Transactions

```go
// ✅ Good: Use the provided transaction
func (e *Executor) ExecuteTask(ctx context.Context, tx pgx.Tx, params *Params) error {
    return e.model.SpawnWithTx(tx).DoWork(ctx, params)
}

// ❌ Bad: Starting a new transaction
func (e *Executor) ExecuteTask(ctx context.Context, tx pgx.Tx, params *Params) error {
    return e.model.RunTransaction(ctx, func(model ModelInterface) error {
        return model.DoWork(ctx, params)
    })
}
```

### 3. Prefer `WithTx` Methods in Transactional Contexts

When you're already within a transaction, always use the `WithTx` variants of other services:

```go
// ✅ Good: Using WithTx methods within transaction
func (s *OrderService) ProcessOrderWithTx(ctx context.Context, tx pgx.Tx, orderID int32) error {
    // Use WithTx variants of other services
    userID, err := s.userService.GetUserByOrderWithTx(ctx, tx, orderID)
    if err != nil {
        return err
    }
    
    // Schedule notification task within the same transaction
    _, err = s.taskRunner.RunOrderNotificationWithTx(ctx, tx, &NotificationParams{
        UserID:  userID,
        OrderID: orderID,
    })
    return err
}

// ❌ Bad: Creating new transactions within existing transaction
func (s *OrderService) ProcessOrderWithTx(ctx context.Context, tx pgx.Tx, orderID int32) error {
    // This creates a separate transaction!
    userID, err := s.userService.GetUserByOrder(ctx, orderID)
    if err != nil {
        return err
    }
    
    // This also creates a separate transaction!
    _, err = s.taskRunner.RunOrderNotification(ctx, &NotificationParams{
        UserID:  userID,
        OrderID: orderID,
    })
    return err
}
```

### 4. Handle Errors Appropriately

```go
func (e *Executor) ExecuteTask(ctx context.Context, tx pgx.Tx, params *Params) error {
    if err := e.validateParams(params); err != nil {
        // Fatal error - don't retry
        return taskcore.ErrFatalTask
    }
    
    if err := e.doWork(ctx, tx, params); err != nil {
        if isTemporaryError(err) {
            // Retryable error
            return err
        }
        // Fatal error
        return taskcore.ErrFatalTask
    }
    
    return nil
}
```

### 5. Design Idempotent Operations

Since tasks are guaranteed to execute at least once, design your task executors to be idempotent:

```go
func (e *Executor) ExecuteProcessPayment(ctx context.Context, tx pgx.Tx, params *PaymentParams) error {
    txModel := e.model.SpawnWithTx(tx)
    
    // Check if already processed (idempotency)
    payment, err := txModel.GetPayment(ctx, params.PaymentID)
    if err != nil {
        return err
    }
    
    if payment.Status == "processed" {
        return nil // Already processed, safe to return success
    }
    
    // Process payment...
    return txModel.UpdatePaymentStatus(ctx, params.PaymentID, "processed")
}
```

## Conclusion: The `WithTx` Pattern as the Foundation

Anchor achieves its goal of being a **single cohesive ultimate backend framework** through the universal application of the `WithTx` pattern. This core principle provides:

### Framework-Wide Consistency
- **Every component** follows the same transaction pattern
- **Every database operation** has both standalone and transactional variants
- **Every layer** (model, service, task, storage) speaks the same transaction language

### Powerful Guarantees
1. **Transactional Composability**: Any operation can be combined with any other operation in a single transaction
2. **At-least-once delivery**: Tasks are guaranteed to be executed through transactional creation and atomic execution
3. **Hook guarantees**: All hooks execute within the same transaction as the triggering operation
4. **Data consistency**: Complex business workflows maintain ACID properties across all components

### Developer Experience
- **Predictable APIs**: If a method exists, its `WithTx` variant also exists
- **Seamless composition**: Operations from different layers can be combined effortlessly
- **Fail-safe design**: Partial failures never leave the system in an inconsistent state
- **Plugin compatibility**: All plugins automatically inherit transactional capabilities

The `WithTx` pattern transforms what could be a collection of separate components into a truly cohesive framework where every piece works together transactionally. This design enables developers to build complex, reliable backend systems with the confidence that data consistency is maintained at every level.

**In essence, `WithTx` is not just a method naming convention—it's the architectural foundation that makes Anchor the ultimate backend framework.**