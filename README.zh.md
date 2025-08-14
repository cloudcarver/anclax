# Anchor 

[English](README.md) | 中文

Anchor 是一个用于构建无服务器和可靠应用程序的框架，能够以闪电般的速度和信心构建应用。

Anchor 提供以下功能：

- [x] 使用 Macaroons 的身份验证和授权
- [x] 具有至少一次交付保证的异步任务管理
- [x] 使用 sqlc 的数据库查询接口
- [x] 使用 Fiber 的 HTTP API 服务器
- [x] 用于轻松扩展框架的插件系统

Anchor 的核心理念是通过以下方式为代码库提供信心：

- 使用 YAML 定义模式并生成接口，以避免缺少实现的运行时错误，这意味着您可以在编译时捕获错误。
- 使用事件驱动架构构建易于理解和扩展的系统。
- 所有模块都是可模拟的，可以轻松测试。

## 文档

- [事务管理](docs/transaction.zh.md) - 了解 Anchor 的 `WithTx` 模式、插件系统，以及事务如何确保至少一次交付和保证钩子执行
- [中间件 (x-functions 和 x-check-rules)](docs/middleware.zh.md) - 学习如何使用 Anchor 的中间件系统实现自定义验证、授权和实用功能

### 异步任务文档

- **[异步任务教程](docs/async-tasks-tutorial.zh.md)** ([English](docs/async-tasks-tutorial.md)) - 用户友好的入门指南，包含异步任务的分步示例
- **[异步任务技术参考](docs/async-tasks-technical.zh.md)** ([English](docs/async-tasks-technical.md)) - 涵盖架构、生命周期和高级功能的全面技术文档

## 快速开始

```bash
go install github.com/cloudcarver/anchor/cmd/anchor@latest
anchor init . github.com/my/app
```

### Wire 注入

Wire 通过匹配构造函数的参数和返回类型来解析依赖。你可以按以下方式获取任意依赖：

- 单例模式（Singleton）：大多数核心服务（如配置、数据库、模型）作为单例提供，确保全局只有一个共享实例，避免重复连接/状态并提升可预测性。
- 随着项目增长，手动初始化并串联所有单例会变得复杂且易错，依赖图会迅速膨胀。
- 使用 Wire 时，你只需在构造函数参数中声明所需依赖，Wire 会自动注入。你可以在 `examples/simple/wire/wire_gen.go` 查看自动生成的初始化代码。

1. 定义一个构造函数（constructor），将所需依赖作为参数声明
2. 在 `examples/simple/wire/wire.go` 的 `wire.Build(...)` 中注册该构造函数
3. 运行 `anchor gen` 生成注入代码

构造函数示例：

```go
// 需要什么就声明什么依赖
func NewGreeter(m model.ModelInterface) (*Greeter, error) {
    return &Greeter{Model: m}, nil
}
```

在 `examples/simple/wire/wire.go` 中注册：

```go
func InitApp() (*app.App, error) {
    wire.Build(
        // ... existing providers ...
        model.NewModel,
        NewGreeter,
    )
    return nil, nil
}
```

当你修改了构造函数或 `wire/wire.go` 后，运行：

```bash
anchor gen
```

1. 使用 YAML 格式定义 HTTP 模式 `api/v1.yaml`。

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

2. 使用 SQL 格式定义数据库模式 `sql/migrations/0001_init.up.sql`。

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

3. 使用 YAML 格式定义任务模式 `api/tasks.yaml`。

  ```yaml
  tasks:
    incrementCounter:
      description: Increment the counter value
      cron: "*/1 * * * *" # 每 1 秒
  ```

4. 运行代码生成。

```
anchor gen
```

5. 实现接口。

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

6. 使用环境变量配置应用程序。

7. 构建并运行应用程序。

## 运行异步任务

在 `api/tasks.yaml` 中定义任务并运行 `anchor gen` 后，框架会自动生成一个任务运行器，为每个任务提供 `Run` 方法。只需调用这些方法即可异步执行任务：

```go
// 触发第 4 步中的 incrementCounter 任务
taskID, err := taskrunner.RunIncrementCounter(ctx, &taskgen.IncrementCounterParameters{
  Amount: 1,
})
```

任务运行时具有至少一次交付保证和基于重试策略配置的自动重试。任务还可以使用任务定义中的 cron 表达式安排自动运行。

## 高级：自定义初始化

通过提供一个在应用启动前调用的 `Init` 函数，你可以在启动阶段执行自定义逻辑。参考 [examples/simple/pkg/init.go](examples/simple/pkg/init.go)。

```go
// 在应用启动之前运行
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

你也可以通过 `InitAnchorApplication` 自定义 Anchor 应用的构建过程：

```go
func InitAnchorApplication(cfg *config.Config) (*anchor_app.Application, error) {
    anchorApp, err := anchor_wire.InitializeApplication(&cfg.Anchor, anchor_config.DefaultLibConfig())
    if err != nil {
        return nil, err
    }
    return anchorApp, nil
}
```

在 `Init` 中需要额外的依赖？只需将其直接声明为参数（例如 `model.ModelInterface`），然后运行 `anchor gen`。详细说明见[Wire 注入](#wire-注入)部分。