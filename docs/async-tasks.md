# Async Tasks in Anchor

Anchor lets you run background tasks that don't block your web requests. For example, you can send emails, process images, or generate reports without making users wait.

## Table of Contents

- [What Are Async Tasks?](#what-are-async-tasks)
- [How to Create a Task](#how-to-create-a-task)
- [How to Write Task Code](#how-to-write-task-code)
- [How to Start Tasks](#how-to-start-tasks)
- [Scheduled Tasks (Cronjobs)](#scheduled-tasks-cronjobs)
- [Error Handling and Events](#error-handling-and-events)
- [Complete Examples](#complete-examples)

## What Are Async Tasks?

Think of async tasks like hiring someone to do work for you later. Instead of doing everything right away when a user makes a request, you can:

1. **Create a task** - Tell Anchor what work needs to be done
2. **Queue it up** - Put the task in a to-do list
3. **Let workers handle it** - Background workers pick up tasks and do the work
4. **Get guarantees** - Tasks will run at least once, even if something goes wrong

Common examples:
- Send welcome emails when users sign up
- Resize images after upload
- Generate monthly reports
- Clean up old data
- Process payments

## Task Definition

Tasks are defined in `api/tasks.yaml` using a structured YAML format:

```yaml
tasks:
  - name: TaskName
    description: "Task description"
    parameters:
      type: object
      required: [param1, param2]
      properties:
        param1:
          type: string
          description: "Parameter description"
        param2:
          type: integer
          format: int32
    retryPolicy:
      interval: 30m
      always_retry_on_failure: true
    cronjob:
      cronExpression: "0 */1 * * *"  # Every hour
    events:
      onFailed: HandleTaskFailure
    timeout: 10m
```

### Task Properties

- **name** (required): Unique task identifier
- **description**: Human-readable task description
- **parameters**: JSON Schema defining task parameters
- **retryPolicy**: Retry configuration for failed tasks
- **cronjob**: Cron scheduling configuration
- **events**: Event handlers for task lifecycle events
- **timeout**: Maximum execution time (default: 1 hour)

### Parameter Types

Parameters follow JSON Schema format:
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

## Task Implementation

After defining tasks, run code generation:

```bash
anchor generate
```

This generates interfaces in `pkg/zgen/taskgen/`:

### Generated Interfaces

```go
// ExecutorInterface - implement this to handle task execution
type ExecutorInterface interface {
    ExecuteTaskName(ctx context.Context, params *TaskNameParameters) error
}

// TaskRunner - use this to enqueue tasks
type TaskRunner interface {
    RunTaskName(ctx context.Context, params *TaskNameParameters, overrides ...taskcore.TaskOverride) (int32, error)
    RunTaskNameWithTx(ctx context.Context, tx pgx.Tx, params *TaskNameParameters, overrides ...taskcore.TaskOverride) (int32, error)
}
```

### Implementing the Executor

Create an executor that implements the generated interface:

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
    // Your task logic here
    return e.model.DoSomething(ctx, params.UserId, params.Amount)
}
```

## Running Tasks

### Enqueuing Tasks

Use the generated TaskRunner to enqueue tasks:

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

### Task Overrides

You can override task properties at runtime:

```go
// Override retry policy
taskID, err := h.taskRunner.RunTaskName(ctx, params, 
    taskcore.WithRetryPolicy("1h", true),
    taskcore.WithTimeout("30m"),
    taskcore.WithUniqueTag("user-123-daily-task"),
)
```

### Transactional Tasks

Enqueue tasks within database transactions:

```go
err := h.model.RunTransaction(ctx, func(txm model.ModelInterface) error {
    // Do some database work
    user, err := txm.GetUser(ctx, userID)
    if err != nil {
        return err
    }
    
    // Enqueue task within the same transaction
    taskID, err := h.taskRunner.RunTaskNameWithTx(ctx, txm.GetTx(), params)
    if err != nil {
        return err
    }
    
    return nil
})
```

## Cronjobs

Define scheduled tasks using cron expressions:

```yaml
tasks:
  - name: DailyCleanup
    description: "Run daily cleanup tasks"
    cronjob:
      cronExpression: "0 0 2 * * *"  # 2 AM daily
    parameters:
      type: object
      properties:
        daysToKeep:
          type: integer
          format: int32
```

Cronjobs support extended cron format with seconds:
- Format: `second minute hour dayOfMonth month dayOfWeek`
- Example: `"*/30 * * * * *"` (every 30 seconds)
- Example: `"0 0 */6 * * *"` (every 6 hours)

## Retry Policies

Configure how tasks should be retried on failure:

```yaml
retryPolicy:
  interval: 30m                    # Wait 30 minutes between retries
  always_retry_on_failure: true    # Always retry on any failure
  max_retries: 3                   # Maximum number of retries (optional)
```

### Retry Intervals

- Simple duration: `"30m"`, `"1h"`, `"5s"`
- Exponential backoff: `"1m,2m,4m,8m"` (comma-separated)

## Error Handling and Events

### Task Failure Events

Tasks can automatically trigger other tasks when they fail using the `events.onFailed` configuration:

```yaml
tasks:
  - name: ProcessPayment
    description: "Process user payment"
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
      always_retry_on_failure: true
    events:
      onFailed: HandlePaymentFailure
      
  - name: HandlePaymentFailure
    description: "Handle failed payment processing"
    # Parameters are optional for failure handlers
    # If not specified, gets a default taskID parameter
```

### How Events Work

1. **Automatic Triggering**: When a task fails permanently (after all retries), the system automatically creates and queues the `onFailed` task
2. **Transaction Safety**: Both the original task status update and the failure task creation happen in the same database transaction
3. **Failure Task Parameters**: The failure handler task receives the ID of the failed task as a parameter
4. **No Retry Interference**: Events are only triggered when tasks fail permanently, not during retries

### Default Parameters for Failure Tasks

If you don't specify parameters for a failure handler task, it automatically gets:

```yaml
parameters:
  type: object
  required: [taskID]
  properties:
    taskID:
      type: integer
      format: int32
      description: "The ID of the task that triggered this event"
```

### Implementing Failure Handlers

```go
func (e *Executor) ExecuteProcessPayment(ctx context.Context, params *taskgen.ProcessPaymentParameters) error {
    // Your payment processing logic
    if err := e.paymentService.ProcessPayment(params.UserId, params.Amount); err != nil {
        // This error will trigger HandlePaymentFailure if retries are exhausted
        return fmt.Errorf("payment processing failed: %w", err)
    }
    return nil
}

func (e *Executor) ExecuteHandlePaymentFailure(ctx context.Context, params *taskgen.HandlePaymentFailureParameters) error {
    // Load the original failed task details
    failedTask, err := e.model.GetTask(ctx, params.TaskID)
    if err != nil {
        return err
    }
    
    // Parse the original task parameters to understand what failed
    var originalParams taskgen.ProcessPaymentParameters
    if err := json.Unmarshal(failedTask.Spec.Payload, &originalParams); err != nil {
        return err
    }
    
    // Handle the failure (notify admin, refund, etc.)
    return e.handlePaymentFailure(ctx, originalParams.UserId, originalParams.Amount, failedTask.ID)
}
```

### Custom Error Handling

In your executor, you can control retry behavior:

```go
func (e *Executor) ExecuteProcessPayment(ctx context.Context, params *taskgen.ProcessPaymentParameters) error {
    // Permanent failure - don't retry, immediately trigger onFailed
    if params.Amount <= 0 {
        return taskcore.ErrFatalTask
    }
    
    // Temporary failure - retry without logging error event
    if rateLimitExceeded {
        return taskcore.ErrRetryTaskWithoutErrorEvent
    }
    
    // Regular error - will retry according to policy
    return processPayment(params)
}
```

## Advanced Features

### Task Timeouts

Configure maximum execution time:

```yaml
tasks:
  - name: LongRunningTask
    timeout: 2h  # 2 hours maximum
```

### Unique Tasks

Prevent duplicate tasks using unique tags:

```go
taskID, err := h.taskRunner.RunTaskName(ctx, params, 
    taskcore.WithUniqueTag(fmt.Sprintf("user-%d-daily", userID)),
)
```

### Task Attributes

Access task metadata in your executor:

```go
func (e *Executor) ExecuteTaskName(ctx context.Context, params *taskgen.TaskNameParameters) error {
    // Get task ID from context (if available)
    if taskID, ok := ctx.Value("taskID").(int32); ok {
        log.Info("Processing task", zap.Int32("taskID", taskID))
    }
    
    return e.processTask(params)
}
```

## Examples

### Example 1: Simple Background Task

**Task Definition:**
```yaml
tasks:
  - name: SendEmail
    description: "Send an email to a user"
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
      always_retry_on_failure: true
```

**Implementation:**
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

**Usage:**
```go
func (h *Handler) RegisterUser(c *fiber.Ctx) error {
    // ... user registration logic
    
    // Send welcome email asynchronously
    _, err := h.taskRunner.RunSendEmail(c.Context(), &taskgen.SendEmailParameters{
        UserId:     user.ID,
        TemplateId: "welcome",
        Variables:  map[string]interface{}{"name": user.Name},
    })
    
    return err
}
```

### Example 2: Scheduled Data Processing

**Task Definition:**
```yaml
tasks:
  - name: ProcessDailyReports
    description: "Generate daily reports"
    cronjob:
      cronExpression: "0 0 1 * * *"  # 1 AM daily
    parameters:
      type: object
      required: [date]
      properties:
        date:
          type: string
          format: date
    retryPolicy:
      interval: 1h
      always_retry_on_failure: true
```

**Implementation:**
```go
func (e *Executor) ExecuteProcessDailyReports(ctx context.Context, params *taskgen.ProcessDailyReportsParameters) error {
    date, err := time.Parse("2006-01-02", params.Date)
    if err != nil {
        return err
    }
    
    // Process reports for the given date
    return e.reportService.GenerateDailyReports(ctx, date)
}
```

### Example 3: Workflow with Failure Events

**Task Definition:**
```yaml
tasks:
  - name: ProcessOrder
    description: "Process customer order"
    parameters:
      type: object
      required: [orderId]
      properties:
        orderId:
          type: integer
          format: int32
    retryPolicy:
      interval: 30m
      always_retry_on_failure: true
    events:
      onFailed: HandleOrderFailure
    timeout: 10m
    
  - name: HandleOrderFailure
    description: "Handle failed order processing"
    # Uses default parameters: { taskID: int32 }
```

**Implementation:**
```go
func (e *Executor) ExecuteProcessOrder(ctx context.Context, params *taskgen.ProcessOrderParameters) error {
    order, err := e.model.GetOrder(ctx, params.OrderId)
    if err != nil {
        return err
    }
    
    // Process the order
    if err := e.orderService.ProcessOrder(ctx, order); err != nil {
        // This will trigger HandleOrderFailure if retries are exhausted
        return err
    }
    
    return nil
}

func (e *Executor) ExecuteHandleOrderFailure(ctx context.Context, params *taskgen.HandleOrderFailureParameters) error {
    // Load the original failed task
    failedTask, err := e.model.GetTask(ctx, params.TaskID)
    if err != nil {
        return err
    }
    
    // Parse original parameters
    var originalParams taskgen.ProcessOrderParameters
    if err := json.Unmarshal(failedTask.Spec.Payload, &originalParams); err != nil {
        return err
    }
    
    // Handle the failure - notify customer service, update order status, etc.
    return e.orderService.HandleFailure(ctx, originalParams.OrderId, failedTask.ID)
}
```

### Example 4: Complex Failure Handling with Custom Parameters

**Task Definition:**
```yaml
tasks:
  - name: SendNotification
    description: "Send notification to user"
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
      always_retry_on_failure: true
    events:
      onFailed: EscalateFailedNotification
      
  - name: EscalateFailedNotification
    description: "Escalate failed notifications to admin"
    parameters:
      type: object
      required: [taskID, escalationLevel]
      properties:
        taskID:
          type: integer
          format: int32
        escalationLevel:
          type: string
          default: "admin"
```

**Implementation:**
```go
func (e *Executor) ExecuteSendNotification(ctx context.Context, params *taskgen.SendNotificationParameters) error {
    return e.notificationService.Send(ctx, params.UserId, params.Message, params.Priority)
}

func (e *Executor) ExecuteEscalateFailedNotification(ctx context.Context, params *taskgen.EscalateFailedNotificationParameters) error {
    // Get the failed task details
    failedTask, err := e.model.GetTask(ctx, params.TaskID)
    if err != nil {
        return err
    }
    
    // Parse original notification parameters
    var originalParams taskgen.SendNotificationParameters
    if err := json.Unmarshal(failedTask.Spec.Payload, &originalParams); err != nil {
        return err
    }
    
    // Escalate to admin with original context
    return e.adminService.EscalateFailedNotification(ctx, EscalationRequest{
        FailedTaskID: params.TaskID,
        OriginalUserId: originalParams.UserId,
        OriginalMessage: originalParams.Message,
        Priority: originalParams.Priority,
        EscalationLevel: params.EscalationLevel,
    })
}
```

### Real-World Example: Delete Operation with Failure Handling

This example from the Anchor codebase shows how to implement a task that deletes sensitive data with proper failure handling:

**Task Definition (api/tasks.yaml):**
```yaml
tasks:
  - name: deleteOpaqueKey
    description: Delete an opaque key
    parameters:
      type: object
      required: [keyID]
      properties:
        keyID:
          type: integer
          format: int64
          description: The ID of the opaque key to delete
    retryPolicy:
      interval: 30m
      always_retry_on_failure: true
    events:
      onFailed: onDeleteOpaqueKeyFailed

  - name: onDeleteOpaqueKeyFailed
    description: Handle failed delete opaque key
    retryPolicy:
      interval: 30m
      always_retry_on_failure: true
    # Uses default parameters: { taskID: int32 }
```

**Generated Types:**
After running `anchor generate`, you get:
```go
type DeleteOpaqueKeyParameters struct {
    KeyID int64 `json:"keyID"`
}

type OnDeleteOpaqueKeyFailedParameters struct {
    TaskID int32 `json:"taskID"`
}
```

**Implementation:**
```go
func (e *Executor) ExecuteDeleteOpaqueKey(ctx context.Context, params *taskgen.DeleteOpaqueKeyParameters) error {
    // Attempt to delete the opaque key
    err := e.model.DeleteOpaqueKey(ctx, params.KeyID)
    if err != nil {
        // If delete fails, this will trigger onDeleteOpaqueKeyFailed after retries
        return fmt.Errorf("failed to delete opaque key %d: %w", params.KeyID, err)
    }
    
    log.Info("Successfully deleted opaque key", zap.Int64("keyID", params.KeyID))
    return nil
}

func (e *Executor) ExecuteOnDeleteOpaqueKeyFailed(ctx context.Context, params *taskgen.OnDeleteOpaqueKeyFailedParameters) error {
    // Load the original failed task
    failedTask, err := e.model.GetTask(ctx, params.TaskID)
    if err != nil {
        return fmt.Errorf("failed to load failed task: %w", err)
    }
    
    // Parse the original parameters
    var originalParams taskgen.DeleteOpaqueKeyParameters
    if err := json.Unmarshal(failedTask.Spec.Payload, &originalParams); err != nil {
        return fmt.Errorf("failed to parse original task parameters: %w", err)
    }
    
    // Handle the failure - could notify administrators, log for manual intervention, etc.
    log.Error("Critical: Failed to delete opaque key after all retries", 
        zap.Int64("keyID", originalParams.KeyID),
        zap.Int32("failedTaskID", params.TaskID))
    
    // Notify security team about failed key deletion
    return e.securityService.NotifyFailedKeyDeletion(ctx, originalParams.KeyID, params.TaskID)
}
```

**Starting the Task:**
```go
func (h *Handler) DeleteKey(c *fiber.Ctx) error {
    keyID := c.Params("id")
    keyIDInt, err := strconv.ParseInt(keyID, 10, 64)
    if err != nil {
        return c.Status(400).JSON(fiber.Map{"error": "Invalid key ID"})
    }
    
    // Queue the deletion task
    taskID, err := h.taskRunner.RunDeleteOpaqueKey(c.Context(), &taskgen.DeleteOpaqueKeyParameters{
        KeyID: keyIDInt,
    })
    if err != nil {
        return err
    }
    
    return c.JSON(fiber.Map{
        "message": "Key deletion queued",
        "taskID": taskID,
    })
}
```

This example demonstrates:
- **Graceful degradation**: If deletion fails, the system doesn't just give up
- **Audit trail**: Failed deletions are logged and tracked
- **Administrative oversight**: Critical failures are escalated to security teams
- **Transactional safety**: Both task status and failure task creation are atomic

## Best Practices

1. **Keep tasks idempotent** - Tasks may be retried, so ensure they can be safely executed multiple times
2. **Use unique tags** - Prevent duplicate tasks for critical operations
3. **Set appropriate timeouts** - Don't let tasks run indefinitely
4. **Handle errors gracefully** - Use specific error types to control retry behavior
5. **Design failure handlers carefully** - Failure tasks should handle cleanup, notifications, or escalations
6. **Monitor task performance** - Use metrics to track task execution times and failure rates
7. **Use transactions** - Enqueue tasks within database transactions for consistency
8. **Test failure scenarios** - Ensure your failure handlers work correctly and don't create infinite loops

## Worker Configuration

The worker runs automatically when you start your Anchor application. You can configure worker behavior:

```go
// Disable worker for specific environments
cfg := &config.Config{
    Worker: config.WorkerConfig{
        Disable: true,  // Disable worker
    },
}
```

Workers poll the database every second for pending tasks and process them with configurable concurrency based on available goroutines.