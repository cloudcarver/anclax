# 中间件：x-functions 和 x-check-rules

[English](middleware.md) | 中文

本文档解释了 Anchor 中的中间件概念，特别是 `x-functions` 和 `x-check-rules`，它们提供了一个强大的代码生成系统，允许您直接在 OpenAPI 规范中编写 Go 代码，并将其编译为类型安全的中间件。

## 概述

Anchor 使用 OpenAPI 3.0 扩展结合独特的方法，**您可以直接在 API 安全作用域中编写实际的 Go 代码**。这种设计避免了对领域特定语言（DSL）的需求，并利用 Go 编译器确保类型安全并在编译时捕获错误。

系统的工作原理：
- **x-check-rules**：定义验证/授权函数的函数签名
- **x-functions**：定义实用函数的函数签名  
- **安全作用域**：包含调用这些函数并传递参数的实际 Go 代码

## 实际工作原理

### 核心机制

1. 您在 `x-check-rules` 和 `x-functions` 中定义函数签名
2. 您在 API 操作的安全作用域中编写**实际的 Go 代码**
3. 代码生成器使用您定义的函数创建 `Validator` 接口
4. 中间件模板直接执行您的 Go 代码

### 示例流程

**API 定义：**
```yaml
# 定义函数签名
x-check-rules:
  OperationPermit:
    description: 检查用户是否有权限执行操作
    useContext: true
    parameters:
      - name: operationID
        description: 操作 ID
        schema:
          type: string

paths:
  /counter:
    post:
      operationId: incrementCounter
      security:
        - BearerAuth:
            # 这是实际的 Go 代码，会被执行！
            - x.OperationPermit(c, operationID)
```

**生成的中间件：**
```go
func (x *XMiddleware) IncrementCounter(c *fiber.Ctx) error {
    if err := x.AuthFunc(c); err != nil {
        return c.Status(fiber.StatusUnauthorized).SendString(err.Error())
    }
    if err := x.PreValidate(c); err != nil {
        return c.Status(fiber.StatusForbidden).SendString(err.Error())
    }
    
    operationID := "IncrementCounter"  // 引用时自动生成
    
    // 您的实际 Go 代码在这里执行：
    if err := x.OperationPermit(c, operationID); err != nil {
        return c.Status(fiber.StatusForbidden).SendString(err.Error())
    }
    
    if err := x.PostValidate(c); err != nil {
        return c.Status(fiber.StatusForbidden).SendString(err.Error())
    }
    return x.ServerInterface.IncrementCounter(c)
}
```

## x-check-rules

`x-check-rules` 定义验证和授权函数的函数签名。这些**不是实现** - 它们只是定义您的 `Validator` 接口中应该存在哪些函数。

### 结构

```yaml
x-check-rules:
  FunctionName:
    description: "此函数的作用"
    useContext: true|false
    parameters:
      - name: parameterName
        description: "参数描述"
        schema:
          type: string
```

### 示例定义

```yaml
x-check-rules:
  OperationPermit:
    description: 检查用户是否有权限执行操作
    useContext: true
    parameters:
      - name: operationID
        description: 要检查权限的操作 ID
        schema:
          type: string
  
  ValidateOrgAccess:
    description: 验证用户是否有权访问指定的组织
    useContext: true
    parameters:
      - name: orgID
        description: 组织 ID
        schema:
          type: integer
          format: int32
      - name: requiredRole
        description: 所需的最低角色
        schema:
          type: string
          enum: ["viewer", "editor", "admin"]
  
  CheckResourceOwnership:
    description: 检查用户是否拥有指定的资源
    useContext: true
    parameters:
      - name: resourceType
        schema:
          type: string
      - name: resourceID
        schema:
          type: integer
          format: int32
```

### 生成的接口

```go
type Validator interface {
    // 标准中间件钩子
    AuthFunc(*fiber.Ctx) error
    PreValidate(*fiber.Ctx) error
    PostValidate(*fiber.Ctx) error
    
    // 从 x-check-rules 生成
    OperationPermit(c *fiber.Ctx, operationID string) error
    ValidateOrgAccess(c *fiber.Ctx, orgID int32, requiredRole string) error
    CheckResourceOwnership(c *fiber.Ctx, resourceType string, resourceID int32) error
}
```

### 在 API 操作中的使用

您在安全作用域中编写**实际的 Go 代码**：

```yaml
paths:
  /orgs/{orgID}/projects/{projectID}:
    get:
      operationId: GetProject
      parameters:
        - name: orgID
          in: path
          required: true
          schema:
            type: integer
            format: int32
        - name: projectID
          in: path
          required: true
          schema:
            type: integer
            format: int32
      security:
        - BearerAuth:
            # 支持多行 Go 代码
            - x.ValidateOrgAccess(c, orgID, "viewer")
            - x.CheckResourceOwnership(c, "project", projectID)
            - x.OperationPermit(c, operationID)
      responses:
        "200":
          description: 项目详情

  /admin/users:
    post:
      operationId: CreateUser
      security:
        - BearerAuth:
            # 复杂表达式也可以工作
            - x.ValidateOrgAccess(c, x.GetCurrentOrgID(c), "admin")
            - x.OperationPermit(c, operationID)
```

## x-functions

`x-functions` 定义可以在安全作用域或应用程序逻辑中调用的实用函数。与 `x-check-rules` 一样，这些定义函数签名，而不是实现。

### 结构

```yaml
x-functions:
  FunctionName:
    description: "此函数的作用"
    useContext: true|false
    params:
      - name: parameterName
        description: "参数描述"
        schema:
          type: string
    return:
      name: returnValueName
      description: "返回值描述"
      schema:
        type: string
```

### 示例定义

```yaml
x-functions:
  GetCurrentOrgID:
    description: 从当前上下文获取组织 ID
    useContext: true
    return:
      name: orgID
      description: 当前组织 ID
      schema:
        type: integer
        format: int32
  
  GetUserRole:
    description: 获取当前用户在指定组织中的角色
    useContext: true
    params:
      - name: orgID
        description: 组织 ID
        schema:
          type: integer
          format: int32
    return:
      name: role
      description: 用户角色
      schema:
        type: string
        enum: ["viewer", "editor", "admin"]
  
  ComputeAccessLevel:
    description: 根据用户角色和资源计算访问级别
    useContext: true
    params:
      - name: resourceType
        schema:
          type: string
      - name: userRole
        schema:
          type: string
    return:
      name: accessLevel
      description: 计算的访问级别
      schema:
        type: string
        enum: ["read", "write", "admin"]
```

### 生成的接口添加

函数被添加到 `Validator` 接口：

```go
type Validator interface {
    // ... 检查规则和标准方法 ...
    
    // 从 x-functions 生成
    GetCurrentOrgID(c *fiber.Ctx) (int32, error)
    GetUserRole(c *fiber.Ctx, orgID int32) (string, error)
    ComputeAccessLevel(c *fiber.Ctx, resourceType string, userRole string) (string, error)
}
```

### 在安全作用域中的使用

您可以在 Go 代码中调用这些函数：

```yaml
paths:
  /orgs/{orgID}/sensitive-data:
    get:
      operationId: GetSensitiveData
      security:
        - BearerAuth:
            # 使用函数动态计算值
            - x.ValidateOrgAccess(c, x.GetCurrentOrgID(c), x.GetUserRole(c, orgID))
            - x.CheckResourceOwnership(c, "sensitive-data", x.GetCurrentOrgID(c))
```

## 实现示例

以下是如何实现 `Validator` 接口：

```go
package main

import (
    "database/sql"
    "errors"
    "fmt"
    "strconv"
    
    "github.com/gofiber/fiber/v2"
    "your-app/pkg/zgen/apigen"
)

type MyValidator struct {
    db     *sql.DB
    logger *log.Logger
}

// 标准中间件钩子
func (v *MyValidator) AuthFunc(c *fiber.Ctx) error {
    token := c.Get("Authorization")
    if token == "" {
        return errors.New("缺少授权头")
    }
    
    // 验证 JWT 并提取声明
    claims, err := validateJWT(token)
    if err != nil {
        return fmt.Errorf("无效令牌: %w", err)
    }
    
    // 存储在上下文中供其他函数使用
    c.Locals("userID", claims.UserID)
    c.Locals("userRole", claims.Role)
    c.Locals("orgID", claims.OrgID)
    
    return nil
}

func (v *MyValidator) PreValidate(c *fiber.Ctx) error {
    // 全局预验证逻辑
    return nil
}

func (v *MyValidator) PostValidate(c *fiber.Ctx) error {
    // 全局后验证逻辑  
    return nil
}

// 实现 x-check-rules
func (v *MyValidator) OperationPermit(c *fiber.Ctx, operationID string) error {
    userRole := c.Locals("userRole").(string)
    
    // 定义操作权限
    permissions := map[string][]string{
        "GetProject":    {"viewer", "editor", "admin"},
        "CreateProject": {"editor", "admin"},
        "DeleteProject": {"admin"},
        "CreateUser":    {"admin"},
    }
    
    allowedRoles, exists := permissions[operationID]
    if !exists {
        return fmt.Errorf("未知操作: %s", operationID)
    }
    
    for _, role := range allowedRoles {
        if userRole == role {
            return nil
        }
    }
    
    return fmt.Errorf("%s 权限不足", operationID)
}

func (v *MyValidator) ValidateOrgAccess(c *fiber.Ctx, orgID int32, requiredRole string) error {
    userID := c.Locals("userID").(int)
    
    var userRole string
    err := v.db.QueryRow(
        "SELECT role FROM org_members WHERE user_id = $1 AND org_id = $2",
        userID, orgID,
    ).Scan(&userRole)
    
    if err == sql.ErrNoRows {
        return errors.New("拒绝访问组织")
    }
    if err != nil {
        return fmt.Errorf("数据库错误: %w", err)
    }
    
    roleHierarchy := map[string]int{
        "viewer": 1,
        "editor": 2, 
        "admin":  3,
    }
    
    if roleHierarchy[userRole] < roleHierarchy[requiredRole] {
        return fmt.Errorf("角色不足: 需要 %s，拥有 %s", requiredRole, userRole)
    }
    
    return nil
}

func (v *MyValidator) CheckResourceOwnership(c *fiber.Ctx, resourceType string, resourceID int32) error {
    userID := c.Locals("userID").(int)
    
    var ownerID int
    query := fmt.Sprintf("SELECT owner_id FROM %ss WHERE id = $1", resourceType)
    err := v.db.QueryRow(query, resourceID).Scan(&ownerID)
    
    if err == sql.ErrNoRows {
        return errors.New("资源未找到")
    }
    if err != nil {
        return fmt.Errorf("数据库错误: %w", err)
    }
    
    if ownerID != userID {
        return errors.New("资源访问被拒绝")
    }
    
    return nil
}

// 实现 x-functions
func (v *MyValidator) GetCurrentOrgID(c *fiber.Ctx) (int32, error) {
    // 首先尝试上下文（来自 JWT）
    if orgID, ok := c.Locals("orgID").(int32); ok && orgID != 0 {
        return orgID, nil
    }
    
    // 尝试路径参数
    if orgIDStr := c.Params("orgID"); orgIDStr != "" {
        orgID, err := strconv.ParseInt(orgIDStr, 10, 32)
        if err != nil {
            return 0, fmt.Errorf("无效的 orgID: %w", err)
        }
        return int32(orgID), nil
    }
    
    return 0, errors.New("未找到组织 ID")
}

func (v *MyValidator) GetUserRole(c *fiber.Ctx, orgID int32) (string, error) {
    userID := c.Locals("userID").(int)
    
    var role string
    err := v.db.QueryRow(
        "SELECT role FROM org_members WHERE user_id = $1 AND org_id = $2",
        userID, orgID,
    ).Scan(&role)
    
    if err == sql.ErrNoRows {
        return "", errors.New("用户不是组织成员")
    }
    if err != nil {
        return "", fmt.Errorf("数据库错误: %w", err)
    }
    
    return role, nil
}

func (v *MyValidator) ComputeAccessLevel(c *fiber.Ctx, resourceType string, userRole string) (string, error) {
    accessMatrix := map[string]map[string]string{
        "project": {
            "viewer": "read",
            "editor": "write", 
            "admin":  "admin",
        },
        "sensitive-data": {
            "viewer": "read",
            "editor": "read",  // 即使编辑者对敏感数据也只有读取权限
            "admin":  "admin",
        },
    }
    
    resourceAccess, exists := accessMatrix[resourceType]
    if !exists {
        return "", fmt.Errorf("未知资源类型: %s", resourceType)
    }
    
    accessLevel, exists := resourceAccess[userRole]
    if !exists {
        return "", fmt.Errorf("未知角色: %s", userRole)
    }
    
    return accessLevel, nil
}
```

## 高级使用模式

### 复杂安全逻辑

```yaml
paths:
  /orgs/{orgID}/projects/{projectID}/secrets:
    get:
      operationId: GetProjectSecrets
      security:
        - BearerAuth:
            # 带函数调用的多步验证
            - x.ValidateOrgAccess(c, orgID, "editor")
            - x.CheckResourceOwnership(c, "project", projectID) 
            # 仅在计算的访问级别为 "admin" 时允许
            - x.ValidateAccessLevel(c, x.ComputeAccessLevel(c, "secrets", x.GetUserRole(c, orgID)), "admin")
```

### 条件逻辑

```yaml
security:
  - BearerAuth:
      # 您甚至可以使用条件逻辑（在验证器中实现）
      - x.ConditionalAccess(c, orgID, projectID, x.GetUserRole(c, orgID))
```

### 变量赋值和重用

系统在引用时自动生成变量：

```yaml
security:
  - BearerAuth:
      # 当您引用 'operationID' 时，它会自动生成为：
      # operationID := "YourOperationName"
      - x.OperationPermit(c, operationID)
      
      # 当您引用路径参数时，它们直接可用：
      - x.ValidateOrgAccess(c, orgID, "viewer")  # orgID 来自路径
```

## 安全方案设置

不要忘记定义您的安全方案：

```yaml
components:
  securitySchemes:
    BearerAuth:
      type: http
      scheme: bearer
      bearerFormat: JWT
```

## 集成

```go
func main() {
    app := fiber.New()
    
    validator := &MyValidator{
        db:     connectDB(),
        logger: log.Default(),
    }
    
    // 验证器同时作为处理器和验证器
    apigen.RegisterHandlersWithOptions(
        app,
        apigen.NewXMiddleware(validator, validator), // validator 实现两个接口
        apigen.FiberServerOptions{},
    )
    
    app.Listen(":8080")
}
```

## 主要优势

1. **无 DSL**：编写实际的 Go 代码，而不是领域特定语言
2. **编译时安全**：Go 编译器捕获安全逻辑中的错误
3. **IDE 支持**：完整的自动完成、重构和调试支持
4. **类型安全**：函数签名由生成的接口强制执行
5. **灵活性**：由于您编写的是真正的 Go 代码，复杂逻辑是可能的
6. **性能**：无运行时解释，只有编译的 Go 代码

## 最佳实践

1. **保持函数专注**：每个函数应该有单一职责
2. **使用有意义的名称**：函数名称应该清楚地表明其目的
3. **错误处理**：始终返回描述性错误消息
4. **上下文使用**：在 Fiber 上下文中存储共享数据以供重用
5. **数据库效率**：在可能时缓存昂贵的查询
6. **测试**：模拟 Validator 接口进行全面测试
7. **文档**：在 YAML 中清晰地记录您的函数签名

这种方法为您提供了 Go 的全部功能，同时保持了 OpenAPI 规范的声明性质，并且您的所有安全逻辑都在构建时编译和类型检查，这是额外的好处。