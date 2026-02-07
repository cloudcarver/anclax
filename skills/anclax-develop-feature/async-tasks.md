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

## Runtime overrides

Use `taskcore.TaskOverride` helpers:
- `taskcore.WithRetryPolicy(interval, maxAttempts)`
- `taskcore.WithCronjob(cronExpression)`
- `taskcore.WithDelay(delay)`
- `taskcore.WithStartedAt(time)`
- `taskcore.WithUniqueTag(tag)`
- `taskcore.WithLabels([]string{"billing", "critical"})`

If a unique tag already exists, the existing task ID is returned instead of inserting a new task.

## Error handling

- Return `taskcore.ErrFatalTask` to skip retries and mark the task failed (hooks still run if configured).
- Return `taskcore.ErrRetryTaskWithoutErrorEvent` to retry without writing a task error event.
- Any other error records a task error event and follows the retry policy.

Cron tasks are rescheduled every run regardless of success or failure.

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
- Workers only claim tasks with matching labels; unlabeled tasks are eligible for all workers.

## Wiring

Wire already registers the async task components in `wire/wire.go`:
- `taskgen.NewTaskHandler`
- `taskgen.NewTaskRunner`
- `asynctask.NewExecutor`

If your executor needs new dependencies, update the Wire providers and run `anclax gen`.
