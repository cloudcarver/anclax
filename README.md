# Anchor 

Anchor is a framework for building serverless and reliable applications in speed of light with confidence.

Anchor provides the following features:

- [x] Authentication & Authorization with Macaroons
- [x] Asynchronous task management with at-least-once delivery
- [x] Database query interface with sqlc
- [x] HTTP API server with Fiber
- [x] Plugin system for easily extending the framework

The core philosophy of Anchor is to provide confidence in the codebase by:

- Use YAML to define schema and generate interfaces to avoid runtime errors of missing implementation, meaning you can catch errors at compile time.
- Use event-driven architecture to build a system that is easy to reason about and easy to extend.
- All modules are mockable and can be tested with ease.

## Quick Start

```bash
go install github.com/cloudcarver/anchor/cmd/anchor@latest
anchor init . github.com/my/app
```

1. Define the HTTP schema `api/v1.yaml` with YAML format.

  ```yaml
  openapi: 3.0.0
  info:
    title: Anchor API
    version: 1.0.0
    description: Anchor API

  paths:
    /api/v1/counter:
      get:
        operationId: getCounter
        summary: Get the counter value
        responses:
          "200":
            description: The counter value
  ```

2. Define the database schema `sql/migrations/0001_init.up.sql` with SQL format.

  ```sql
  CREATE TABLE IF NOT EXISTS counter (
    value INTEGER NOT NULL DEFAULT 0
  );
  ```

  ```sql
  -- name: GetCounter :one
  SELECT value FROM counter LIMIT 1;

  -- name: IncrementCounter :exec
  UPDATE counter SET value = value + 1;
  ```

3. Define the task schema `api/tasks.yaml` with YAML format.

  ```yaml
  tasks:
    incrementCounter:
      description: Increment the counter value
      cron: "*/1 * * * *" # every 1 seconds
  ```

4. Run code generation.

```
anchor gen
```

5. Implement the interfaces.

  ```go
  func (h *Handler) GetCounter(c *fiber.Ctx) error {
    return c.JSON(apigen.Counter{Count: 0})
  }
  ```

  ```go
  func (e *Executor) IncrementCounter(ctx context.Context, params *IncrementCounterParameters) error {
    return e.model.IncrementCounter(ctx)
  }
  ```

6. Configure the application using environment variables.

7. Build and run the application.

## Running Async Tasks

After defining tasks in `api/tasks.yaml` and running `anchor gen`, the framework auto-generates a task runner with `Run` methods for each task. Simply call these methods to execute tasks asynchronously:

```go
// Trigger the incrementCounter task from step 4
taskID, err := taskrunner.RunIncrementCounter(ctx, &taskgen.IncrementCounterParameters{
  Amount: 1,
})
```

Tasks run with at-least-once delivery guarantees and automatic retries based on your retry policy configuration. Tasks can also be scheduled to run automatically using cron expressions in the task definition.
