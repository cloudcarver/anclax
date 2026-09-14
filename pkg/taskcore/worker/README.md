# worker

The task worker separates scheduling decisions from asynchronous effects while supporting deterministic distributed-system tests.

## Boundaries

- `Engine`: pure `Event -> []Command` state machine; owns business/strict/control capacity, weighted group selection, manual request admission, and execution phases.
- `Runtime`: sole engine owner; drives timers and operations, routes `RunTask` through admission, and drains finalization on shutdown.
- `Port`: side-effect boundary. `ModelPort` uses short PostgreSQL transactions and invokes task handlers outside them. Optional `ControlPort` and `TaskLookupPort` extend existing adapters.
- `lifecycle_policy.go`: pure retry, cron, interruption, and deferral decisions.
- `lifecycle_handler.go`: fenced atomic outcome/event persistence and savepoint-isolated failure hooks.
- `task_execution.go`: per-attempt cancellation, lease renewal, and execution registry keyed by task ID and lease version.
- `lease_manager.go`: shared deadline scheduler and bounded batch renewal through the model's isolated renewal pool.
- `Worker`: public facade, constructed using `BuildWorkerComponents` and `NewWorker`, or `NewWorkerFromConfig`.

## Execution

A cycle advances through claim, execute, and finalize. Business slots remain reserved through finalization. Manual and polled tasks share that budget; the strict limit applies to both. Strict-to-normal fallback retains its reservation until the database rechecks task priority and selects a task. Built-in control tasks use one separate slot in production and yield durable deferrals while waiting for acknowledgements.

Polling fills idle capacity and completion triggers refill. Empty claims wait for another poll. Stop rejects new admission, cancels execution, and gives finalization its own bounded context before marking the worker offline.

## Deterministic tests

Use `Engine.Apply(event)` directly to control event ordering. Use `Runtime.Step(ctx, event)` with a test `Port` to include side effects; `Step` submits a scheduling event but does not wait for all asynchronous effects. Production `Runtime.Start` additionally owns polling and automatic refill.

See [worker leases](../../../docs/async-task-worker-lease.md) for persistence invariants, migration requirements, configuration, and PostgreSQL regressions.
