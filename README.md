# Anchor 

English | [中文](README.zh.md)

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

## Documentation

- [Transaction Management](docs/transaction.md) ([中文](docs/transaction.zh.md)) - Learn about Anchor's `WithTx` pattern, plugin system, and how transactions ensure at-least-once delivery and guaranteed hook execution
- [Middleware (x-functions and x-check-rules)](docs/middleware.md) ([中文](docs/middleware.zh.md)) - Learn how to implement custom validation, authorization, and utility functions using Anchor's middleware system

### Async Tasks Documentation

- **[Async Tasks Tutorial](docs/async-tasks-tutorial.md)** ([中文](docs/async-tasks-tutorial.zh.md)) - User-friendly guide with step-by-step examples for getting started with async tasks
- **[Async Tasks Technical Reference](docs/async-tasks-technical.md)** ([中文](docs/async-tasks-technical.zh.md)) - Comprehensive technical documentation covering architecture, lifecycle, and advanced features

## Quick Start

```bash
go install github.com/cloudcarver/anchor/cmd/anchor@latest
anchor init . github.com/my/app
```

### Wire Injection

Wire resolves dependencies by matching constructor parameters and return types. You can get anything you need by:

1. Defining a constructor with the dependencies you want as parameters
2. Registering that constructor in `examples/simple/wire/wire.go` inside `wire.Build(...)`
3. Running `anchor gen` to generate the injection code

Example constructor:

```go
// Any dependencies you need are declared as parameters
func NewGreeter(m model.ModelInterface) (*Greeter, error) {
    return &Greeter{Model: m}, nil
}
```

Register it in `examples/simple/wire/wire.go`:

```go
func InitApp() (*app.App, error) {
    wire.Build(
        // ... existing providers ...
        model.NewModel,
        NewGreeter,
        pkg.Init,
    )
    return nil, nil
}
```

Use dependencies in `Init` by declaring them as parameters and regenerate:

```go
// Add what you need, e.g., model.ModelInterface
func Init(anchorApp *anchor_app.Application, taskrunner taskgen.TaskRunner, m model.ModelInterface, myapp anchor_app.Plugin) (*app.App, error) {
    // use m here
    return &app.App{AnchorApp: anchorApp}, nil
}
```

After editing constructors, `wire/wire.go`, or `Init` parameters, run:

```bash
anchor gen
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


## Advanced: Custom Initialization

Customize application startup by providing an `Init` function that runs before the app starts. See [examples/simple/pkg/init.go](examples/simple/pkg/init.go).

```go
// Runs before the application starts
func Init(anchorApp *anchor_app.Application, taskrunner taskgen.TaskRunner, myapp anchor_app.Plugin) (*app.App, error) {
    if err := anchorApp.Plug(myapp); err != nil {
        return nil, err
    }

    if _, err := anchorApp.GetService().CreateNewUser(context.Background(), "test", "test"); err != nil {
        return nil, err
    }
    if _, err := taskrunner.RunAutoIncrementCounter(context.Background(), &taskgen.AutoIncrementCounterParameters{
        Amount: 1,
    }, taskcore.WithUniqueTag("auto-increment-counter")); err != nil {
        return nil, err
    }

    return &app.App{ AnchorApp: anchorApp }, nil
}
```

To control how the Anchor application is constructed, you can also customize `InitAnchorApplication`:

```go
func InitAnchorApplication(cfg *config.Config) (*anchor_app.Application, error) {
    anchorApp, err := anchor_wire.InitializeApplication(&cfg.Anchor, anchor_config.DefaultLibConfig())
    if err != nil {
        return nil, err
    }
    return anchorApp, nil
}
```

Need additional dependencies inside `Init`? Add them directly as parameters (for example, `model.ModelInterface`), then run `anchor gen`. See the [Wire injection](#wire-injection) section for details.
