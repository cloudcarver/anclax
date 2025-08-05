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

Anchor provides a powerful async task system with at-least-once delivery guarantees. Tasks can be triggered manually from HTTP endpoints or scheduled as cron jobs.

### 1. Define Tasks in `api/tasks.yaml`

```yaml
tasks:
  - name: SendEmail
    description: Send email notification to user
    retryPolicy:
      interval: 5m
      maxAttempts: 3
    parameters:
      type: object
      required: [email, subject, body]
      properties:
        email:
          type: string
          format: email
        subject:
          type: string
        body:
          type: string
  - name: ProcessData
    description: Process data every hour
    cronjob:
      cronExpression: "0 * * * *"
    retryPolicy:
      interval: 10m
      maxAttempts: 5
    parameters:
      type: object
      required: [dataId]
      properties:
        dataId:
          type: integer
          format: int32
```

### 2. Implement Task Executors

After running `anchor gen`, implement the generated executor interfaces:

```go
// pkg/asynctask/asynctask.go
func (e *Executor) ExecuteSendEmail(ctx context.Context, tx pgx.Tx, params *taskgen.SendEmailParameters) error {
    // Send email logic here
    log.Printf("Sending email to %s with subject: %s", params.Email, params.Subject)
    
    // Your email sending implementation
    return e.emailService.Send(ctx, params.Email, params.Subject, params.Body)
}

func (e *Executor) ExecuteProcessData(ctx context.Context, tx pgx.Tx, params *taskgen.ProcessDataParameters) error {
    // Process data logic here
    log.Printf("Processing data with ID: %d", params.DataId)
    
    // Your data processing implementation
    return e.dataProcessor.Process(ctx, params.DataId)
}
```

### 3. Trigger Tasks from HTTP Handlers

```go
// pkg/handler/handler.go
func (h *Handler) SendNotification(c *fiber.Ctx) error {
    var req struct {
        Email   string `json:"email"`
        Subject string `json:"subject"`
        Body    string `json:"body"`
    }
    
    if err := c.BodyParser(&req); err != nil {
        return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request"})
    }
    
    // Submit async task
    taskID, err := h.taskrunner.RunSendEmail(c.Context(), &taskgen.SendEmailParameters{
        Email:   req.Email,
        Subject: req.Subject,
        Body:    req.Body,
    })
    if err != nil {
        return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
    }
    
    return c.JSON(fiber.Map{
        "message": "Email task submitted",
        "taskId":  taskID,
    })
}
```

### 4. Task Overrides and Options

You can customize task behavior using overrides:

```go
import "github.com/cloudcarver/anchor/pkg/taskcore"

// Run task with custom retry policy
taskID, err := h.taskrunner.RunSendEmail(c.Context(), 
    &taskgen.SendEmailParameters{
        Email:   "user@example.com",
        Subject: "Welcome",
        Body:    "Welcome to our service!",
    },
    taskcore.WithRetryPolicy("1m", 5), // Retry every 1 minute, max 5 attempts
    taskcore.WithUniqueTag("welcome-email-user123"), // Prevent duplicate tasks
)
```

### 5. Transaction Support

Run tasks within database transactions:

```go
func (h *Handler) CreateUserAndSendWelcome(c *fiber.Ctx) error {
    return h.model.WithTx(c.Context(), func(ctx context.Context, tx pgx.Tx) error {
        // Create user in database
        userID, err := h.model.CreateUser(ctx, userData)
        if err != nil {
            return err
        }
        
        // Submit welcome email task in same transaction
        _, err = h.taskrunner.RunSendEmailWithTx(ctx, tx, &taskgen.SendEmailParameters{
            Email:   userData.Email,
            Subject: "Welcome!",
            Body:    fmt.Sprintf("Welcome user %d!", userID),
        })
        
        return err
    })
}
```

### Key Features

- **At-least-once delivery**: Tasks are guaranteed to execute at least once
- **Automatic retries**: Failed tasks are retried based on retry policy
- **Cron scheduling**: Tasks can run on schedule using cron expressions
- **Transaction support**: Tasks can be submitted within database transactions
- **Unique tags**: Prevent duplicate task execution
- **Monitoring**: Built-in metrics and task status tracking
