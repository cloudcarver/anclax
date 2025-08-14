# ⚓ Anchor

English | [中文](README.zh.md)

Build serverless, reliable apps at lightspeed ⚡ — with confidence 🛡️.

### Highlights ✨

- **YAML-first, codegen-backed**: Define HTTP and task schemas in YAML; Anchor generates strongly-typed interfaces so missing implementations fail at compile time, not in prod.
- **Async tasks you can trust**: At-least-once delivery, automatic retries, and cron scheduling out of the box.
- **Transaction-safe flows**: A `WithTx` pattern ensures hooks always run and side effects are consistent.
- **Typed database layer**: Powered by `sqlc` for safe, fast queries.
- **Fast HTTP server**: Built on Fiber for performance and ergonomics.
- **AuthN/Z built-in**: Macaroons-based authentication and authorization.
- **Pluggable architecture**: First-class plugin system for clean modularity.
- **Ergonomic DI**: Wire-based dependency injection keeps code testable and explicit.

### Why Anchor? (The problem it solves) 🤔

- **Glue-code fatigue**: Many teams stitch HTTP, DB, tasks, DI, and auth by hand, leaving implicit contracts and runtime surprises. Anchor makes those contracts explicit and generated.
- **Background jobs are hard**: Idempotency, retries, and delivery guarantees are non-trivial. Anchor ships a task engine with at-least-once semantics and cron.
- **Consistency across boundaries**: Keep handlers, tasks, and hooks transactional using `WithTx` so invariants hold.
- **Confidence and testability**: Every generated interface is mockable; behavior is easy to test.

### Key advantages 🏆

- **Compile-time confidence**: Schema → interfaces → concrete implementations you cannot forget to write.
- **Productivity**: `anchor init` + `anchor gen` reduces boilerplate and wiring.
- **Extensibility**: Clean plugin boundaries and event-driven architecture.
- **Predictability**: Singletons for core services, DI for clarity, and well-defined lifecycles.

## Quick start 🚀

```bash
go install github.com/cloudcarver/anchor/cmd/anchor@latest
anchor init . github.com/my/app
anchor gen
```

## One‑minute tour 🧭

1) Define an endpoint (OpenAPI YAML) 🧩

```yaml
paths:
  /api/v1/counter:
    get:
      operationId: getCounter
```

2) Define a task ⏱️

```yaml
tasks:
  incrementCounter:
    description: Increment the counter value
    cron: "*/1 * * * *"
```

3) Generate and implement 🛠️

```bash
anchor gen
```

```go
func (h *Handler) GetCounter(c *fiber.Ctx) error {
  return c.JSON(apigen.Counter{Count: 0})
}
```

## Showcase: unique features 🧰

### OpenAPI-powered middleware (no DSL)
```yaml
x-check-rules:
  OperationPermit:
    useContext: true
    parameters:
      - name: operationID
        schema:
          type: string
  ValidateOrgAccess:
    useContext: true
    parameters:
      - name: orgID
        schema:
          type: integer
          format: int32

paths:
  /orgs/{orgID}/projects/{projectID}:
    get:
      operationId: GetProject
      security:
        - BearerAuth:
            - x.ValidateOrgAccess(c, orgID, "viewer")
            - x.OperationPermit(c, operationID)
```

### Security scheme (JWT example)
```yaml
components:
  securitySchemes:
    BearerAuth:
      type: http
      scheme: bearer
      bearerFormat: macaroon
```

### Async tasks: at-least-once, retries, cron
```yaml
# api/tasks.yaml
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
    cron: "0 * * * *"
```

```go
// Enqueue outside a tx
taskID, _ := taskrunner.RunSendWelcomeEmail(ctx, &taskgen.SendWelcomeEmailParameters{
  UserId: 123, TemplateId: "welcome",
}, taskcore.WithUniqueTag("welcome-email:123"))
```

```go
// Enqueue atomically with your business logic
_ = model.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
  // ... create user ...
  _, err := taskrunner.RunSendWelcomeEmailWithTx(ctx, tx, &taskgen.SendWelcomeEmailParameters{
    UserId: user.ID, TemplateId: "welcome",
  })
  return err
})
```

### Transactions: compose everything with WithTx
```go
func (s *Service) CreateUserWithTx(ctx context.Context, tx pgx.Tx, username, password string) (int32, error) {
  txm := s.model.SpawnWithTx(tx)
  userID, err := txm.CreateUser(ctx, username, password)
  if err != nil { return 0, err }
  if err := s.hooks.OnUserCreated(ctx, tx, userID); err != nil { return 0, err }
  _, err = s.taskRunner.RunSendWelcomeEmailWithTx(ctx, tx, &taskgen.SendWelcomeEmailParameters{ UserId: userID })
  return userID, err
}
```

### Dependency injection with Wire
```go
func NewGreeter(m model.ModelInterface) (*Greeter, error) { return &Greeter{Model: m}, nil }
```

```go
func InitApp() (*app.App, error) {
  wire.Build(model.NewModel, NewGreeter /* ...other providers... */)
  return nil, nil
}
```

### Typed SQL with sqlc
```sql
-- name: GetCounter :one
SELECT value FROM counter LIMIT 1;

-- name: IncrementCounter :exec
UPDATE counter SET value = value + 1;
```

## Documentation 📚

- **Transaction Management**: [docs/transaction.md](docs/transaction.md) ([中文](docs/transaction.zh.md))
- **Middleware (x-functions & x-check-rules)**: [docs/middleware.md](docs/middleware.md) ([中文](docs/middleware.zh.md))
- **Async Tasks**: Tutorial [docs/async-tasks-tutorial.md](docs/async-tasks-tutorial.md) · Tech reference [docs/async-tasks-technical.md](docs/async-tasks-technical.md) ([中文](docs/async-tasks-tutorial.zh.md), [中文](docs/async-tasks-technical.zh.md))

## Examples 🧪

- `examples/simple` — minimal end-to-end sample with HTTP, tasks, DI, and DB.

## Deep dive (original full guide) 🔎

Prefer the detailed step-by-step? Read the archived full guide:

- English: `docs/README-full.md`
- 中文：`docs/README.zh-full.md`
