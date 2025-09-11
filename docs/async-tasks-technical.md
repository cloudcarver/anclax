# Async Tasks in Anclax

English | [中文](async-tasks-technical.zh.md)

> 🚀 **New to async tasks?** Start with the [Tutorial Guide](async-tasks-tutorial.md) for step-by-step examples and practical usage patterns.

This document provides a comprehensive overview of Anclax's async task system, covering both the user experience flow and the underlying technical mechanisms.

## Table of Contents

- [Overview](#overview)
- [User Experience Flow](#user-experience-flow)
- [Underlying Architecture](#underlying-architecture)
- [Task Lifecycle](#task-lifecycle)
- [Advanced Features](#advanced-features)
- [Performance and Reliability](#performance-and-reliability)

## Overview

Anclax's async task system provides a robust, reliable way to execute background work with at-least-once delivery guarantees. The system is designed around a simple principle: define tasks declaratively, implement them in code, and let the framework handle all the complexity of queuing, retrying, and monitoring.

### Key Benefits

- **At-least-once delivery**: Tasks are guaranteed to execute successfully at least once
- **Automatic retries**: Failed tasks are retried according to configurable policies
- **Type safety**: Full compile-time type checking for task parameters
- **Transaction support**: Tasks can be enqueued within database transactions
- **Cron scheduling**: Tasks can run on schedules using cron expressions
- **Failure hooks**: Automatic cleanup and notification when tasks fail permanently

## User Experience Flow

### 1. Task Definition Phase

Users start by defining tasks in `api/tasks.yaml` using a declarative YAML format:

```yaml
tasks:
  - name: SendWelcomeEmail
    description: Send welcome email to new users
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

**What happens behind the scenes:**
- The framework validates the YAML schema
- Task definitions are parsed and stored for code generation
- Parameter schemas are validated against JSON Schema standards

### 2. Code Generation Phase

Users run `anclax gen` to generate type-safe interfaces:

```bash
anclax gen
```

**What happens behind the scenes:**
- The code generator reads all task definitions
- Generates strongly-typed parameter structs
- Creates `ExecutorInterface` with methods for each task
- Creates `TaskRunner` interface for enqueueing tasks
- Generates mock implementations for testing

**Generated code example:**
```go
// Generated parameter struct
type SendWelcomeEmailParameters struct {
    UserId     int32  `json:"userId"`
    TemplateId string `json:"templateId"`
}

// Generated executor interface
type ExecutorInterface interface {
    ExecuteSendWelcomeEmail(ctx context.Context, tx pgx.Tx, params *SendWelcomeEmailParameters) error
}

// Generated task runner interface
type TaskRunner interface {
    RunSendWelcomeEmail(ctx context.Context, params *SendWelcomeEmailParameters, overrides ...taskcore.TaskOverride) (int32, error)
    RunSendWelcomeEmailWithTx(ctx context.Context, tx pgx.Tx, params *SendWelcomeEmailParameters, overrides ...taskcore.TaskOverride) (int32, error)
}
```

### 3. Implementation Phase

Users implement the generated executor interface:

```go
func (e *Executor) ExecuteSendWelcomeEmail(ctx context.Context, tx pgx.Tx, params *taskgen.SendWelcomeEmailParameters) error {
    user, err := e.model.GetUser(ctx, params.UserId)
    if err != nil {
        return err
    }
    
    return e.emailService.SendWelcomeEmail(user.Email, params.TemplateId)
}
```

**What happens behind the scenes:**
- The executor is registered with the task worker system
- The framework maps task types to executor methods
- Parameter validation occurs automatically before method invocation

### 4. Task Execution Phase

Users trigger tasks from their application code:

```go
// From an HTTP handler
func (h *Handler) RegisterUser(c *fiber.Ctx) error {
    // ... user registration logic ...
    
    // Enqueue welcome email task
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

**What happens behind the scenes:**
- Task parameters are serialized to JSON
- A task record is inserted into the database
- The task is marked as `pending`
- A task ID is returned immediately
- Background workers pick up and execute the task

## Underlying Architecture

### Database Schema

The async task system uses several database tables:

```sql
-- Core task table
CREATE TABLE anclax_tasks (
    id SERIAL PRIMARY KEY,
    spec JSONB NOT NULL,           -- Task type and parameters
    attributes JSONB NOT NULL,     -- Retry policy, timeout, etc.
    status TEXT NOT NULL,          -- pending, running, completed, failed
    started_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    unique_tag TEXT UNIQUE         -- For preventing duplicates
);

-- Cron job scheduling
CREATE TABLE anclax_cron_jobs (
    id SERIAL PRIMARY KEY,
    task_id INTEGER REFERENCES anclax_tasks(id),
    cron_expression TEXT NOT NULL,
    next_run TIMESTAMP NOT NULL,
    enabled BOOLEAN DEFAULT true
);
```

### Worker Architecture

The worker system consists of several components:

#### 1. Task Store Interface
```go
type TaskStoreInterface interface {
    PushTask(ctx context.Context, task *apigen.Task) (int32, error)
    PullTask(ctx context.Context) (*apigen.Task, error)
    UpdateTaskStatus(ctx context.Context, taskID int32, status string) error
    // ... other methods
}
```

#### 2. Worker Pool
- Workers run as goroutines within the main application process
- Each worker polls for pending tasks every second
- Configurable concurrency based on available system resources
- Graceful shutdown handling

#### 3. Task Execution Flow
```
1. Worker calls PullTask() to get next pending task
2. Task status updated to "running"
3. Worker deserializes task parameters
4. Worker calls appropriate executor method
5. On success: status updated to "completed"
6. On failure: retry logic kicks in or failure hooks are triggered
```

### Retry Mechanism

The retry system implements exponential backoff with jitter:

```go
type RetryPolicy struct {
    Interval    string `json:"interval"`    // "5m" or "1m,2m,4m,8m"
    MaxAttempts int    `json:"maxAttempts"` // -1 for unlimited
}
```

**Retry Algorithm:**
1. Parse interval string (simple duration or comma-separated list)
2. Calculate next retry time based on attempt number
3. Add jitter to prevent thundering herd
4. Update task with next execution time
5. Worker picks up task when retry time arrives

### Transaction Safety

Tasks can be enqueued within database transactions:

```go
func (s *Service) CreateUserWithWelcomeEmail(ctx context.Context, userData UserData) error {
    return s.model.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
        // Create user
        user, err := s.model.CreateUser(ctx, userData)
        if err != nil {
            return err
        }
        
        // Enqueue welcome email in same transaction
        _, err = s.taskRunner.RunSendWelcomeEmailWithTx(ctx, tx, &taskgen.SendWelcomeEmailParameters{
            UserId:     user.ID,
            TemplateId: "welcome",
        })
        
        return err
    })
}
```

**Transaction Guarantees:**
- If user creation fails, welcome email task is not enqueued
- If task enqueueing fails, user creation is rolled back
- Both operations succeed or both fail atomically

## Task Lifecycle

### State Transitions

```
pending → running → completed
    ↓         ↓
    ↓    → failed → pending (retry)
    ↓              ↓
    ↓         → failed (permanent)
    ↓              ↓
    ↓         → hook execution
    ↓
    → cancelled (manual intervention)
```

### Detailed Lifecycle

1. **Task Creation**
   - Task definition validated
   - Parameters serialized
   - Database record created with status `pending`
   - Unique tag checked (if provided)

2. **Task Pickup**
   - Worker queries for oldest pending task
   - Task status updated to `running`
   - Worker process begins execution

3. **Task Execution**
   - Parameters deserialized and validated
   - Executor method invoked with context and transaction
   - Execution time monitored against timeout

4. **Success Path**
   - Task status updated to `completed`
   - Metrics updated
   - Task removed from active processing

5. **Failure Path**
   - Error logged and categorized
   - Retry policy consulted
   - If retries remaining: status → `pending`, next_run updated
   - If retries exhausted: status → `failed`, failure hooks triggered

6. **Failure Hook Execution**
   - Hook method invoked within transaction
   - Original task parameters passed to hook
   - Hook success/failure affects final task status

### Cron Job Lifecycle

Scheduled tasks follow a different lifecycle:

1. **Cron Job Registration**
   - Cron expression parsed and validated
   - Next execution time calculated
   - Job registered in scheduler

2. **Scheduled Execution**
   - When next_run time arrives, new task instance created
   - Task follows normal execution lifecycle
   - Next execution time recalculated

3. **Cron Job Management**
   - Jobs can be paused/resumed
   - Cron expressions can be updated
   - Jobs can be deleted

## Advanced Features

### Task Overrides

Runtime customization of task behavior:

```go
taskID, err := taskRunner.RunSendWelcomeEmail(ctx, params,
    taskcore.WithRetryPolicy("1h", 5),           // Custom retry
    taskcore.WithTimeout("2m"),                  // Custom timeout
    taskcore.WithUniqueTag("user-123-welcome"),  // Prevent duplicates
    taskcore.WithDelay(time.Hour),               // Delay execution
)
```

**Override Implementation:**
- Overrides are applied as functional options
- They modify the task attributes before database insertion
- Type-safe validation ensures override compatibility

### Failure Hooks

Automatic cleanup and notification system:

```yaml
tasks:
  - name: ProcessPayment
    # ... other config ...
    events:
      - onFailed
```

**Hook Mechanism:**
- Hooks are only triggered on permanent failures
- Hooks receive original task parameters with full type safety
- Hooks execute within the same transaction as status update
- Hook failures are logged but don't affect task status

### Unique Tasks

Prevent duplicate task execution:

```go
// This will succeed
taskID1, _ := taskRunner.RunSendWelcomeEmail(ctx, params,
    taskcore.WithUniqueTag("user-123-welcome"))

// This will fail with ErrTaskAlreadyExists
taskID2, _ := taskRunner.RunSendWelcomeEmail(ctx, params,
    taskcore.WithUniqueTag("user-123-welcome"))
```

**Uniqueness Implementation:**
- Unique tags stored in database with unique constraint
- Duplicate detection happens at database level
- Failed duplicates return specific error type

## Performance and Reliability

### Scalability Characteristics

- **Horizontal scaling**: Multiple application instances can run workers
- **Database bottleneck**: All coordination happens through database
- **Polling overhead**: Workers poll every second (configurable)
- **Memory usage**: Minimal - tasks are not kept in memory

### Reliability Guarantees

- **At-least-once delivery**: Guaranteed by database persistence and retry logic
- **Crash recovery**: Tasks survive application restarts
- **Transaction safety**: Task enqueueing respects transaction boundaries
- **Timeout protection**: Runaway tasks are terminated

### Monitoring and Observability

The system exposes metrics for monitoring:

```go
// Prometheus metrics
var (
    TasksEnqueued = promauto.NewCounter(...)
    TasksCompleted = promauto.NewCounter(...)
    TasksFailed = promauto.NewCounter(...)
    TaskExecutionDuration = promauto.NewHistogram(...)
)
```

**Available Metrics:**
- Task enqueue rate
- Task completion rate  
- Task failure rate
- Execution duration distribution
- Queue depth
- Worker utilization

### Best Practices

1. **Design for Idempotency**
   - Tasks may be executed multiple times
   - Use database transactions or unique constraints
   - Check current state before making changes

2. **Handle Partial Failures**
   - Break large tasks into smaller units
   - Use saga pattern for complex workflows
   - Implement proper rollback logic

3. **Monitor and Alert**
   - Set up alerts for high failure rates
   - Monitor queue depth for capacity planning
   - Track execution times for performance regression

4. **Test Failure Scenarios**
   - Test retry behavior under various failure conditions
   - Verify failure hooks work correctly
   - Ensure graceful degradation

5. **Resource Management**
   - Set appropriate timeouts
   - Limit concurrent task execution
   - Monitor memory and CPU usage

6. **Use Async Tasks for Module Decoupling**
   - Decouple modules by using async tasks instead of direct method calls
   - For example, when an order is paid, instead of calling all factory operations directly in `finishOrder()`, enqueue an `orderFinished` task
   - This keeps the `finishOrder` method concise and allows factory-specific logic to be defined within the factory module
   - Results in cleaner code that's easier to debug and maintain
   - **Important**: Only use this pattern for eventual consistency scenarios, not for strong consistency requirements like real-time financial transactions between accounts

```go
// Instead of this (tightly coupled):
func (o *OrderService) FinishOrder(ctx context.Context, orderID int32) error {
    // Update order status
    if err := o.model.UpdateOrderStatus(ctx, orderID, "completed"); err != nil {
        return err
    }
    
    // Directly call factory operations (tight coupling)
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

// Do this instead (decoupled with async tasks):
func (o *OrderService) FinishOrder(ctx context.Context, orderID int32) error {
    return o.model.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
        // Update order status
        if err := o.model.UpdateOrderStatus(ctx, orderID, "completed"); err != nil {
            return err
        }
        
        // Enqueue async task for factory operations (loose coupling)
        _, err := o.taskRunner.RunOrderFinishedWithTx(ctx, tx, &taskgen.OrderFinishedParameters{
            OrderId: orderID,
        })
        
        return err
    })
}

// Factory module handles its own logic independently
func (f *FactoryExecutor) ExecuteOrderFinished(ctx context.Context, tx pgx.Tx, params *taskgen.OrderFinishedParameters) error {
    // All factory-specific logic contained within factory module
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

This comprehensive system provides a robust foundation for asynchronous task processing while maintaining simplicity for developers through its declarative configuration and type-safe interfaces.