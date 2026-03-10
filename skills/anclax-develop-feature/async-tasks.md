# Async Tasks

Use async tasks for background work with at-least-once delivery, retries, cron schedules, and failure hooks.

## Developer orientation

Use this checklist before diving into implementation details. It prevents chasing scattered code paths.

Understand the system at a top level:
- Tasks are defined in a spec, then code is generated. Treat generated code as the layer contract.
- Enqueueing, worker execution, retry decisions, and events are separate responsibilities.
- Status transitions and retries are persisted in the database, not in memory.

Check in this order when debugging or adding features:
1. Task definitions and defaults (retry, timeout, cron) in the spec/config.
2. Generated interfaces (runner/executor) that your code must implement or call.
3. Task store helpers (enqueue/update/status lookups/waiting utilities).
4. Worker lifecycle (claim/lock, execute, retry, finalize).
5. Event emission and hooks for failures.
6. Queries/migrations that define persistence behavior.
7. Tests/examples that capture edge cases.

## Define tasks

`api/tasks.yaml` is the source of truth. Parameters follow JSON Schema.

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
    cronjob:
      cronExpression: "0 0 * * * *" # seconds minute hour day month weekday
    events:
      - onFailed
    timeout: 10m
```

Notes:
- Cron uses a 6-field format including seconds.
- Omit `cronjob` for one-off tasks.

## Generate code

Run `anclax gen` after task changes. Generated code lives in `pkg/zgen/taskgen`.

## Implement the executor

Implement `taskgen.ExecutorInterface`. Task execution runs outside a DB transaction; open a short transaction inside the handler if needed.

The implementation should be idempotent, task is delivered at-least once.

```go
type Executor struct {
    model model.ModelInterface
    email EmailService
}

func (e *Executor) ExecuteSendWelcomeEmail(ctx context.Context, params *taskgen.SendWelcomeEmailParameters) error {
    user, err := e.model.GetUser(ctx, params.UserId)
    if err != nil {
        return err
    }
    return e.email.SendWelcomeEmail(user.Email, params.TemplateId)
}

func (e *Executor) OnSendWelcomeEmailFailed(ctx context.Context, taskID int32, params *taskgen.SendWelcomeEmailParameters, tx core.Tx) error {
    return nil
}
```

## Enqueue tasks

Use the generated `taskgen.TaskRunner`:

```go
taskID, err := taskRunner.RunSendWelcomeEmail(ctx, &taskgen.SendWelcomeEmailParameters{
    UserId: 123,
    TemplateId: "welcome",
})
```

To enqueue inside a transaction:

```go
err := model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
    _, err := taskRunner.RunSendWelcomeEmailWithTx(ctx, tx, &taskgen.SendWelcomeEmailParameters{
        UserId: userID,
        TemplateId: "welcome",
    })
    return err
})
```

## Pause/cancel tasks (worker control plane)

Use the worker control plane to pause or cancel tasks and interrupt in-flight execution.

Current flow (task-driven, backend-agnostic):
- Marks the target task status in storage (`paused` or `cancelled`).
- Cascades to descendants (`parentTaskId` chain) in the same transaction.
- Enqueues broadcast control tasks via the task system (not Postgres LISTEN/NOTIFY).
- Fanout child tasks are idempotent (unique tags + `parent_task_id`).
- ACK/NACK is inferred from child task terminal states, polled with `ackPollInterval`.
- If no workers are alive, control plane skips broadcast enqueue/wait.

Example:
```go
if err := controlPlane.PauseTask(ctx, taskID); err != nil {
    return err
}

if err := controlPlane.CancelTask(ctx, taskID); err != nil {
    return err
}
```

## Runtime overrides

Use `taskcore.TaskOverride` helpers:
- `taskcore.WithRetryPolicy(interval, maxAttempts)`
- `taskcore.WithCronjob(cronExpression)`
- `taskcore.WithDelay(delay)`
- `taskcore.WithStartedAt(time)`
- `taskcore.WithUniqueTag(tag)`
- `taskcore.WithParentTaskID(parentID)`
- `taskcore.WithLabels([]string{"billing", "critical"})`
- `taskcore.WithSerialKey("order-42")`
- `taskcore.WithSerialID(7)`

If a unique tag already exists, the existing task ID is returned instead of inserting a new task.

## Error handling

- Return `taskcore.ErrFatalTask` to skip retries and mark the task failed (hooks still run if configured).
- Return `taskcore.ErrRetryTaskWithoutErrorEvent` to retry without writing a task error event.
- Any other error records a task error event and follows the retry policy.

Cron tasks are rescheduled every run regardless of success or failure.

## Serial execution

Set `serialKey` to force tasks with the same key to run one-by-one. Optionally set `serialID` for explicit ordering.

Ordering policy:
- If any pending tasks for a key have `serialID`, the smallest `serialID` is always the head of the chain.
- If no tasks have `serialID`, order by `created_at`, then `started_at` (NULL first), then `id`.

Claim gating and corner cases:
- A task with `serialID` but no `serialKey` is rejected when enqueuing.
- Empty `serialKey` is rejected.
- The head of the chain blocks all other tasks for the same key, even if its `started_at` is in the future.
- `started_at` only controls eligibility; it does not reorder the serial chain.
- Mixed tasks: if any task has `serialID`, tasks without `serialID` wait until all `serialID` tasks for that key complete.

## Worker leases and labels

- Tasks are claimed in a short transaction, then executed outside the transaction.
- Locks use `locked_at` + TTL and are refreshed while executing.
- If a lock is lost, the worker skips final status updates.

Worker config keys:
- `worker.pollinterval`
- `worker.concurrency`
- `worker.heartbeatInterval`
- `worker.lockTtl`
- `worker.lockRefreshInterval`
- `worker.labels`
- `worker.workerId`

Task labels:
- Add `labels` to `api/tasks.yaml` task definitions.
- Claiming uses **all-match** semantics for business labels: every task label must exist on the worker.
- Unlabeled tasks are eligible for all workers.
- Each worker always includes an internal `worker:<workerId>` label.
- A worker with no business labels (only internal `worker:<workerId>`) can claim only:
  - unlabeled tasks, and
  - tasks labeled with its own `worker:<workerId>`.

Example:
- Task labels: `["gpu", "arm"]`
- Worker labels `["gpu"]` → cannot claim
- Worker labels `["gpu", "arm"]` → can claim

## Wiring

Wire already registers the async task components in `wire/wire.go`:
- `taskgen.NewTaskHandler`
- `taskgen.NewTaskRunner`
- `asynctask.NewExecutor`

If your executor needs new dependencies, update the Wire providers and run `anclax gen`.
