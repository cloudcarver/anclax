# Middleware: x-functions and x-check-rules

English | [中文](middleware.zh.md)

This document explains the middleware concepts in Anclax, specifically `x-functions` and `x-check-rules`, which provide a powerful code generation system that allows you to write Go code directly in your OpenAPI specification that gets compiled into type-safe middleware.

## Overview

Anclax uses OpenAPI 3.0 extensions combined with a unique approach where **you write actual Go code directly in your API security scopes**. This design prevents the need for a Domain Specific Language (DSL) and leverages the Go compiler to ensure type safety and catch errors at compile time.

The system works by:
- **x-check-rules**: Define the function signatures for validation/authorization functions
- **x-functions**: Define the function signatures for utility functions  
- **Security scopes**: Contain actual Go code that calls these functions with parameters

## How It Actually Works

### The Core Mechanism

1. You define function signatures in `x-check-rules` and `x-functions`
2. You write **actual Go code** in the security scopes of your API operations
3. The code generator creates a `Validator` interface with your defined functions
4. The middleware template executes your Go code directly

### Example Flow

**API Definition:**
```yaml
# Define the function signature
x-check-rules:
  OperationPermit:
    description: Check if the user has permission to perform the operation
    useContext: true
    parameters:
      - name: operationID
        description: The operation ID
        schema:
          type: string

paths:
  /counter:
    post:
      operationId: incrementCounter
      security:
        - BearerAuth:
            # This is actual Go code that gets executed!
            - x.OperationPermit(c, operationID)
```

**Generated Middleware:**
```go
func (x *XMiddleware) IncrementCounter(c *fiber.Ctx) error {
    if err := x.AuthFunc(c); err != nil {
        return c.Status(fiber.StatusUnauthorized).SendString(err.Error())
    }
    if err := x.PreValidate(c); err != nil {
        return c.Status(fiber.StatusForbidden).SendString(err.Error())
    }
    
    operationID := "IncrementCounter"  // Auto-generated when referenced
    
    // Your actual Go code gets executed here:
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

`x-check-rules` define the function signatures for validation and authorization functions. These are **not the implementations** - they just define what functions should exist in your `Validator` interface.

### Structure

```yaml
x-check-rules:
  FunctionName:
    description: "What this function does"
    useContext: true|false
    parameters:
      - name: parameterName
        description: "Parameter description"
        schema:
          type: string
```

### Example Definition

```yaml
x-check-rules:
  OperationPermit:
    description: Check if the user has permission to perform the operation
    useContext: true
    parameters:
      - name: operationID
        description: The operation ID to check permissions for
        schema:
          type: string
  
  ValidateOrgAccess:
    description: Validate that the user has access to the specified organization
    useContext: true
    parameters:
      - name: orgID
        description: The organization ID
        schema:
          type: integer
          format: int32
      - name: requiredRole
        description: The minimum role required
        schema:
          type: string
          enum: ["viewer", "editor", "admin"]
  
  CheckResourceOwnership:
    description: Check if user owns the specified resource
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

### Generated Interface

```go
type Validator interface {
    // Standard middleware hooks
    AuthFunc(*fiber.Ctx) error
    PreValidate(*fiber.Ctx) error
    PostValidate(*fiber.Ctx) error
    
    // Generated from x-check-rules
    OperationPermit(c *fiber.Ctx, operationID string) error
    ValidateOrgAccess(c *fiber.Ctx, orgID int32, requiredRole string) error
    CheckResourceOwnership(c *fiber.Ctx, resourceType string, resourceID int32) error
}
```

### Usage in API Operations

You write **actual Go code** in the security scopes:

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
            # Multiple lines of Go code are supported
            - x.ValidateOrgAccess(c, orgID, "viewer")
            - x.CheckResourceOwnership(c, "project", projectID)
            - x.OperationPermit(c, operationID)
      responses:
        "200":
          description: Project details

  /admin/users:
    post:
      operationId: CreateUser
      security:
        - BearerAuth:
            # Complex expressions work too
            - x.ValidateOrgAccess(c, x.GetCurrentOrgID(c), "admin")
            - x.OperationPermit(c, operationID)
```

## x-functions

`x-functions` define utility functions that can be called within your security scopes or application logic. Like `x-check-rules`, these define function signatures, not implementations.

### Structure

```yaml
x-functions:
  FunctionName:
    description: "What this function does"
    useContext: true|false
    params:
      - name: parameterName
        description: "Parameter description"
        schema:
          type: string
    return:
      name: returnValueName
      description: "Return value description"
      schema:
        type: string
```

### Example Definition

```yaml
x-functions:
  GetCurrentOrgID:
    description: Get the organization ID from the current context
    useContext: true
    return:
      name: orgID
      description: The current organization ID
      schema:
        type: integer
        format: int32
  
  GetUserRole:
    description: Get the current user's role in the specified organization
    useContext: true
    params:
      - name: orgID
        description: The organization ID
        schema:
          type: integer
          format: int32
    return:
      name: role
      description: The user's role
      schema:
        type: string
        enum: ["viewer", "editor", "admin"]
  
  ComputeAccessLevel:
    description: Compute access level based on user role and resource
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
      description: The computed access level
      schema:
        type: string
        enum: ["read", "write", "admin"]
```

### Generated Interface Addition

Functions are added to the `Validator` interface:

```go
type Validator interface {
    // ... check rules and standard methods ...
    
    // Generated from x-functions
    GetCurrentOrgID(c *fiber.Ctx) (int32, error)
    GetUserRole(c *fiber.Ctx, orgID int32) (string, error)
    ComputeAccessLevel(c *fiber.Ctx, resourceType string, userRole string) (string, error)
}
```

### Usage in Security Scopes

You can call these functions in your Go code:

```yaml
paths:
  /orgs/{orgID}/sensitive-data:
    get:
      operationId: GetSensitiveData
      security:
        - BearerAuth:
            # Use functions to compute values dynamically
            - x.ValidateOrgAccess(c, x.GetCurrentOrgID(c), x.GetUserRole(c, orgID))
            - x.CheckResourceOwnership(c, "sensitive-data", x.GetCurrentOrgID(c))
```

## Implementation Example

Here's how you implement the `Validator` interface:

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

// Standard middleware hooks
func (v *MyValidator) AuthFunc(c *fiber.Ctx) error {
    token := c.Get("Authorization")
    if token == "" {
        return errors.New("missing authorization header")
    }
    
    // Validate JWT and extract claims
    claims, err := validateJWT(token)
    if err != nil {
        return fmt.Errorf("invalid token: %w", err)
    }
    
    // Store in context for other functions to use
    c.Locals("userID", claims.UserID)
    c.Locals("userRole", claims.Role)
    c.Locals("orgID", claims.OrgID)
    
    return nil
}

func (v *MyValidator) PreValidate(c *fiber.Ctx) error {
    // Global pre-validation logic
    return nil
}

func (v *MyValidator) PostValidate(c *fiber.Ctx) error {
    // Global post-validation logic  
    return nil
}

// Implement x-check-rules
func (v *MyValidator) OperationPermit(c *fiber.Ctx, operationID string) error {
    userRole := c.Locals("userRole").(string)
    
    // Define operation permissions
    permissions := map[string][]string{
        "GetProject":    {"viewer", "editor", "admin"},
        "CreateProject": {"editor", "admin"},
        "DeleteProject": {"admin"},
        "CreateUser":    {"admin"},
    }
    
    allowedRoles, exists := permissions[operationID]
    if !exists {
        return fmt.Errorf("unknown operation: %s", operationID)
    }
    
    for _, role := range allowedRoles {
        if userRole == role {
            return nil
        }
    }
    
    return fmt.Errorf("insufficient permissions for %s", operationID)
}

func (v *MyValidator) ValidateOrgAccess(c *fiber.Ctx, orgID int32, requiredRole string) error {
    userID := c.Locals("userID").(int)
    
    var userRole string
    err := v.db.QueryRow(
        "SELECT role FROM org_members WHERE user_id = $1 AND org_id = $2",
        userID, orgID,
    ).Scan(&userRole)
    
    if err == sql.ErrNoRows {
        return errors.New("access denied to organization")
    }
    if err != nil {
        return fmt.Errorf("database error: %w", err)
    }
    
    roleHierarchy := map[string]int{
        "viewer": 1,
        "editor": 2, 
        "admin":  3,
    }
    
    if roleHierarchy[userRole] < roleHierarchy[requiredRole] {
        return fmt.Errorf("insufficient role: need %s, have %s", requiredRole, userRole)
    }
    
    return nil
}

func (v *MyValidator) CheckResourceOwnership(c *fiber.Ctx, resourceType string, resourceID int32) error {
    userID := c.Locals("userID").(int)
    
    var ownerID int
    query := fmt.Sprintf("SELECT owner_id FROM %ss WHERE id = $1", resourceType)
    err := v.db.QueryRow(query, resourceID).Scan(&ownerID)
    
    if err == sql.ErrNoRows {
        return errors.New("resource not found")
    }
    if err != nil {
        return fmt.Errorf("database error: %w", err)
    }
    
    if ownerID != userID {
        return errors.New("resource access denied")
    }
    
    return nil
}

// Implement x-functions
func (v *MyValidator) GetCurrentOrgID(c *fiber.Ctx) (int32, error) {
    // Try context first (from JWT)
    if orgID, ok := c.Locals("orgID").(int32); ok && orgID != 0 {
        return orgID, nil
    }
    
    // Try path parameter
    if orgIDStr := c.Params("orgID"); orgIDStr != "" {
        orgID, err := strconv.ParseInt(orgIDStr, 10, 32)
        if err != nil {
            return 0, fmt.Errorf("invalid orgID: %w", err)
        }
        return int32(orgID), nil
    }
    
    return 0, errors.New("organization ID not found")
}

func (v *MyValidator) GetUserRole(c *fiber.Ctx, orgID int32) (string, error) {
    userID := c.Locals("userID").(int)
    
    var role string
    err := v.db.QueryRow(
        "SELECT role FROM org_members WHERE user_id = $1 AND org_id = $2",
        userID, orgID,
    ).Scan(&role)
    
    if err == sql.ErrNoRows {
        return "", errors.New("user not member of organization")
    }
    if err != nil {
        return "", fmt.Errorf("database error: %w", err)
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
            "editor": "read",  // Even editors only get read access to sensitive data
            "admin":  "admin",
        },
    }
    
    resourceAccess, exists := accessMatrix[resourceType]
    if !exists {
        return "", fmt.Errorf("unknown resource type: %s", resourceType)
    }
    
    accessLevel, exists := resourceAccess[userRole]
    if !exists {
        return "", fmt.Errorf("unknown role: %s", userRole)
    }
    
    return accessLevel, nil
}
```

## Advanced Usage Patterns

### Complex Security Logic

```yaml
paths:
  /orgs/{orgID}/projects/{projectID}/secrets:
    get:
      operationId: GetProjectSecrets
      security:
        - BearerAuth:
            # Multi-step validation with function calls
            - x.ValidateOrgAccess(c, orgID, "editor")
            - x.CheckResourceOwnership(c, "project", projectID) 
            # Only allow if computed access level is "admin"
            - x.ValidateAccessLevel(c, x.ComputeAccessLevel(c, "secrets", x.GetUserRole(c, orgID)), "admin")
```

### Conditional Logic

```yaml
security:
  - BearerAuth:
      # You can even use conditional logic (implement in your validator)
      - x.ConditionalAccess(c, orgID, projectID, x.GetUserRole(c, orgID))
```

### Variable Assignment and Reuse

The system automatically generates variables when referenced:

```yaml
security:
  - BearerAuth:
      # When you reference 'operationID', it gets auto-generated as:
      # operationID := "YourOperationName"
      - x.OperationPermit(c, operationID)
      
      # When you reference path parameters, they're available directly:
      - x.ValidateOrgAccess(c, orgID, "viewer")  # orgID from path
```

## Security Schemes Setup

Don't forget to define your security schemes:

```yaml
components:
  securitySchemes:
    BearerAuth:
      type: http
      scheme: bearer
      bearerFormat: JWT
```

## Integration

```go
func main() {
    app := fiber.New()
    
    validator := &MyValidator{
        db:     connectDB(),
        logger: log.Default(),
    }
    
    // The validator serves as both the handler and validator
    apigen.RegisterHandlersWithOptions(
        app,
        apigen.NewXMiddleware(validator, validator), // validator implements both interfaces
        apigen.FiberServerOptions{},
    )
    
    app.Listen(":8080")
}
```

## Key Benefits

1. **No DSL**: Write actual Go code, not a domain-specific language
2. **Compile-time safety**: Go compiler catches errors in your security logic
3. **IDE support**: Full autocomplete, refactoring, and debugging support
4. **Type safety**: Function signatures are enforced by the generated interface
5. **Flexibility**: Complex logic is possible since you're writing real Go code
6. **Performance**: No runtime interpretation, just compiled Go code

## Best Practices

1. **Keep functions focused**: Each function should have a single responsibility
2. **Use meaningful names**: Function names should clearly indicate their purpose
3. **Error handling**: Always return descriptive error messages
4. **Context usage**: Store shared data in Fiber context for reuse
5. **Database efficiency**: Cache expensive queries when possible
6. **Testing**: Mock the Validator interface for comprehensive testing
7. **Documentation**: Document your function signatures clearly in the YAML

This approach gives you the full power of Go while maintaining the declarative nature of OpenAPI specifications, with the added benefit that all your security logic is compiled and type-checked at build time.