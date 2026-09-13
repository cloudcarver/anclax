# Async Tasks in Anclax

English | [中文](async-tasks-technical.zh.md)

> 🚀 **New to async tasks?** Start with the [Tutorial Guide](async-tasks-tutorial.md) for step-by-step usage.
>
> ⚖️ **Need scheduling internals?** See [Scheduling & Runtime Config Guide](async-task-scheduling-runtime-config.md) for strict/normal lane semantics, `WithPriority`/`WithWeight`, and runtime propagation flow.

This document provides a comprehensive overview of Anclax's async task system, covering both the user experience flow and the underlying technical mechanisms.

## Table of Contents

- [Overview](#overview)
- [User Experience Flow](#user-experience-flow)
- [Underlying Architecture](#underlying-architecture)
- [Task Lifecycle](#task-lifecycle)
- [Scheduling: Priority, Weight, and Runtime Config](#scheduling-priority-weight-and-runtime-config)
- [Advanced Features](#advanced-features)
- [Performance and Reliability](#performance-and-reliability)

## Overview

Anclax's async task system provides a robust, reliable way to execute background work with at-least-once delivery guarantees. The system is designed around a simple principle: define tasks declaratively, implement them in code, and let the framework handle all the complexity of queuing, retrying, and monitoring.

### Developer Orientation (Read Before Diving In)

If you're extending or debugging async tasks, start with the high-level concepts and checkpoints below.
This prevents chasing scattered code paths and assumptions.

**What you should understand first:**
- **Task definition and generation**: tasks are declared in a spec, then code is generated. Always confirm what is generated and what is hand-written.
- **Runtime responsibilities**: task enqueueing, worker execution, retry decisions, and events are separate concerns with different owners.
- **Persistence contract**: tasks and events live in the database; status transitions and retries are persisted, not in-memory.

**What to check (in order):**
1. **Specs and config**: task definitions, retry policy defaults, timeouts, and generation settings.
2. **Generated interfaces**: TaskRunner and Executor APIs that your code must implement or call.
3. **Task store layer**: enqueueing, updating status, and helper utilities (e.g., wait-for-completion).
4. **Worker lifecycle**: claiming/locking, executing, error handling, and retries.
5. **Events and hooks**: how TaskError events are emitted and when failure hooks run.
6. **Database queries**: authoritative behavior for task selection, retries, and event lookup.
7. **Tests and examples**: validate behavior assumptions and discover edge cases.

**Guiding principles:**
- Treat generated code as the contract between layers; do not hand-edit it.
- Treat SQL and specs as the source of truth for persistence and API types.
- When behavior seems unclear, start from data flow (task record → worker → event) rather than searching for a single entry point.

### Key Benefits

- **At-least-once delivery**: Tasks are guaranteed to execute successfully at least once
- **Automatic retries**: Failed tasks are retried according to configurable policies
- **Type safety**: Full compile-time type checking for task parameters
- **Transaction support**: Tasks can be enqueued within database transactions
- **Cron scheduling**: Tasks can run on schedules using cron expressions
- **Failure hooks**: Automatic cleanup and notification when tasks fail permanently

## User Experience Flow

### 1. Task Definition Phase

Users start by defining tasks in `api/tasks/tasks.yaml` using a declarative YAML format:

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

Task state lives in `anclax.tasks`; events, worker membership, and versioned scheduling configuration live in `anclax.events`, `anclax.workers`, and `anclax.worker_runtime_configs`. Cron metadata is stored in task attributes and `started_at`. The schema source is `sql/migrations`.

The worker has four boundaries:

- `Engine` is a pure `Event -> []Command` state machine. It owns admission, strict capacity, weighted group rotation, and manual execution requests.
- `Runtime` owns the event loop, timers, asynchronous operations, cancellation, and bounded shutdown. Automatic polling and `RunTask` share capacity through finalization.
- `ModelPort` performs short database operations and invokes handlers outside transactions. The execution registry is keyed by `(task ID, lease version)`.
- The lifecycle policy computes an outcome without I/O. The lifecycle handler persists that outcome, events, and failure hooks in one transaction.

A poll fills available business slots; completion triggers another fill. An empty claim waits for the next poll. The default business concurrency is 10. Framework control tasks have one additional independent slot per worker.

### Transaction Safety

Use generated `Run*WithTx` methods with `model.RunTransactionWithTx` to commit business changes and task enqueueing together. The executor itself does not hold a framework database transaction. Failure hooks receive the finalization transaction through `core.Tx`.

## Task Lifecycle

### Claim, execution, and finalization

1. Claim a due `pending` task with `FOR UPDATE SKIP LOCKED`, respecting labels, serial order, priority, and group eligibility. Database time determines lease expiry.
2. Set `worker_id` and `locked_at`; increment `lease_version` and `attempts`; commit before executing. A leased task remains `pending` in storage; the lease identifies active execution.
3. Invoke the handler with a cancellable context and optional task timeout. Renew the lease while running. Executor panics enter the failure path.
4. Atomically finalize using task ID, worker ID, and lease version. Clear the lease and record the applicable event. A committed pause/cancel wins over a late result; a stale lease cannot write back.

Normal success becomes `completed`. Failure remains `pending` with a later `started_at` while retries remain, otherwise it becomes `failed`. Completed, failed, and cancelled tasks are terminal. Resume applies only to paused tasks and invalidates the old attempt; an outstanding old lease may need to expire before the resumed task is claimable.

### Retry and durable deferral

Retry intervals are fixed positive Go durations such as `5s` or `1m`. `maxAttempts` includes the initial attempt; a negative value allows unlimited attempts. `ErrFatalTask` bypasses retries. `ErrRetryTaskWithoutErrorEvent` suppresses the event while retrying.

Returning `taskcore.DeferTask(delay)` schedules another invocation without consuming an attempt or emitting a failure event. It releases the execution slot and persists the next check time. Work done before deferring must be idempotent. Worker shutdown interruption uses the same rescheduling path. Ordinary task timeouts count as failures.

### Cron lifecycle

A cron task reuses the same row and task ID. Retries belong to the current occurrence. After success or exhausted retries, schedule the next six-field cron occurrence and reset `attempts` to zero. A failed occurrence invokes the failure hook and records an error even when no retry policy exists; future occurrences remain scheduled. Pause and cancel stop future scheduling.

### Failure hooks and shutdown

`OnTaskFailed` runs when an occurrence exhausts retries or returns a fatal error. A savepoint isolates hook SQL errors and panics: hook changes roll back, while the task outcome and event can commit. Database/commit failures still propagate.

Shutdown stops admission, cancels executors, and drains their finalization using a context independent of the caller's cancellation. Database operations and the shutdown drain default to five-second bounds. Worker offline marking follows drained operations, including startup registration and heartbeat. An executor that ignores cancellation can outlive that bound; expired leases permit recovery. Delivery remains at least once, so external side effects must be idempotent.

## Scheduling: Priority, Weight, and Runtime Config

### Lane semantics

Built-in config, pause, cancel, and broadcast tasks use an independent control lane. The priority rules below apply to business tasks.

- **Strict lane**: tasks with `priority > 0`
  - claimed first when strict slots are available
  - ordered by `priority DESC`, then `created_at ASC`, then `id ASC`
- **Normal lane**: tasks with `priority == 0`
  - selected through weighted label-group rotation
  - group-level fairness is controlled by runtime `labelWeights`

Strict lane capacity is bounded by:

```text
strict_cap = ceil(concurrency * maxStrictPercentage / 100)
```

### Task-level controls

- `taskcore.WithPriority(priority int32)`
  - validates `priority >= 0`
- `taskcore.WithWeight(weight int32)`
  - validates `weight >= 1`
  - affects normal-lane ordering within a selected group (`weight DESC`)

### Runtime worker config update

The built-in task `broadcastUpdateWorkerRuntimeConfig` writes versioned config rows and fans worker-control command tasks out to the alive worker snapshot.

Flow summary:
1. Idempotently get or create a version in `anclax.worker_runtime_configs` by request ID.
2. Enqueue `applyWorkerRuntimeConfigToWorker` for each remote target worker; local workers can be signaled directly.
3. Workers claim their command tasks by `worker:<id>` label, refresh latest config, apply atomically, and update `workers.applied_config_version` monotonically.
4. Convergence is determined from DB lagging-worker state.
5. If a newer config version appears while waiting, the older broadcast exits as superseded.

For runnable examples and operational guidance, see:
- [Scheduling & Runtime Config Guide](async-task-scheduling-runtime-config.md)
- [Async Task Worker Lease Design](async-task-worker-lease.md)
- [Async Task Testing for Production Readiness](async-task-testing-production-readiness.md)

### Worker control task requests

Worker control-plane messages are durable tasks with reserved types, claimed through the independent control lane:

- `broadcastUpdateWorkerRuntimeConfig` fans out `applyWorkerRuntimeConfigToWorker`.
- `broadcastCancelTask` fans out `cancelTaskOnWorker`.
- `broadcastPauseTask` fans out `pauseTaskOnWorker`.

Broadcast tasks snapshot alive workers, enqueue one worker-targeted command task per remote worker, and wait for command tasks or DB convergence depending on the operation. Worker-targeted command tasks use `worker:<id>` labels and unique tags so each target worker claims its own command.

After `InterruptTasks`, control handlers check whether the target executions have finalized. If any remain, they return `DeferTask`, persisting the next check and releasing the control slot. Broadcast acknowledgement checks use the same mechanism. Registry entries close at the end of `FinalizeTask` for the matching lease version. The control-plane caller still waits for convergence, while workers release capacity between checks. Deferred invocations reuse request IDs, configuration versions, and per-worker child unique tags.

When adding a new worker-control request:
1. **Define task schema** in `api/tasks/tasks.yaml`.
2. **Regenerate** generated task code with `anclax gen`.
3. **Add broadcast executor logic** that snapshots target workers and enqueues worker-targeted command tasks.
4. **Handle in worker** via `WorkerControlTaskHandler`, updating the control type lists in `worker.IsControlTask` and the SQL claim queries together.
5. **Add tests** for fanout, local-worker fast path, worker-target filtering, duplicate command behavior, and wait/convergence semantics.

## Advanced Features

### Task Overrides

Runtime customization of task behavior:

```go
taskID, err := taskRunner.RunSendWelcomeEmail(ctx, params,
    taskcore.WithRetryPolicy("1h", 5),           // Custom retry
    taskcore.WithTimeout("2m"),                  // Custom timeout
    taskcore.WithUniqueTag("user-123-welcome"),  // Prevent duplicates
    taskcore.WithParentTaskID(parentID),         // Link to parent task
    taskcore.WithDelay(time.Hour),               // Delay execution
)
```

**Override Implementation:**
- Overrides are applied as functional options
- They modify the task attributes before database insertion
- Type-safe validation ensures override compatibility

### Task Hierarchies and Control-Plane Interrupts

Tasks may include an optional `parentTaskId` to form a hierarchy. Use `taskcore.WithParentTaskID` when enqueueing child tasks. When the control plane receives a `PauseTask` or `CancelTask` request, it now applies the status change to the target task and all descendants inside the same transaction, then enqueues a single interrupt task that contains the full list of task IDs.

### Waiting for Task Completion

Sometimes you need to block until a task finishes (for tests, orchestration, or CLI workflows).
WorkerControlPlane provides a wait helper that waits for terminal states and includes failure context.

```go
ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()

err := controlPlane.WaitForTask(ctx, taskID)
```

**How it works:**
- Waits until the task reaches `completed`, `failed`, or `cancelled`.
- On failure, reads the most recent TaskError event and returns a message that includes:
  - the task attempt count
  - the retry policy max attempts
  - the last error message from the TaskError event
- On cancellation, returns an error wrapping `ErrTaskCancelled`.
- On timeout or context cancellation, returns the context error.

**Implementation references:**
- `pkg/taskcore/ctrl/ctrl.go` implements the public wait helper.
- `pkg/taskcore/listener` implements the internal task listener.
- `sql/queries/tasks.sql` defines wait status and task error queries.

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
- Hooks run on permanent task failures or failed cron occurrences
- Hooks receive original task parameters with full type safety
- Hooks execute within the status-update transaction, isolated by a savepoint
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
