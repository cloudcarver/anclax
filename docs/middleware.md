# Middleware: x-functions and x-check-rules

This document explains the middleware concepts in Anchor, specifically `x-functions` and `x-check-rules`, which provide powerful ways to extend your API with custom validation, authorization, and utility functions.

## Overview

Anchor uses OpenAPI 3.0 extensions to define middleware behavior that gets automatically generated into Go code. These extensions allow you to:

- **x-check-rules**: Define authorization and validation rules that run before your API endpoints
- **x-functions**: Define utility functions that can be called within your application logic

Both concepts are defined in your `api/v1.yaml` file and are automatically code-generated into middleware that wraps your API handlers.

## x-check-rules

`x-check-rules` define validation and authorization rules that are executed as middleware before your API endpoints. These rules help enforce security policies, validate business logic, and control access to operations.

### Structure

```yaml
x-check-rules:
  RuleName:
    description: "Description of what this rule does"
    useContext: true|false
    parameters:
      - name: parameterName
        description: "Parameter description"
        schema:
          type: string|integer|boolean|object
          # Additional schema properties
```

### Properties

- **description**: Human-readable description of the rule
- **useContext**: Whether the rule needs access to the Fiber context (`*fiber.Ctx`)
- **parameters**: Array of parameters the rule accepts

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
        description: The organization ID to validate access for
        schema:
          type: integer
          format: int32
      - name: requiredRole
        description: The minimum role required
        schema:
          type: string
          enum: ["viewer", "editor", "admin"]
```

### Generated Interface

The code generator creates a `Validator` interface that you must implement:

```go
type Validator interface {
    // AuthFunc is called before the request is processed
    AuthFunc(*fiber.Ctx) error
    
    // PreValidate is called before the request is processed
    PreValidate(*fiber.Ctx) error
    
    // PostValidate is called after the request is processed
    PostValidate(*fiber.Ctx) error
    
    // Generated from x-check-rules
    OperationPermit(c *fiber.Ctx, operationID string) error
    ValidateOrgAccess(c *fiber.Ctx, orgID int32, requiredRole string) error
}
```

### Usage in API Operations

To use check rules in your API operations, reference them in the security section:

```yaml
paths:
  /api/v1/orgs/{orgID}/users:
    get:
      operationId: ListOrgUsers
      summary: List users in an organization
      security:
        - BearerAuth: []
        - ValidateOrgAccess: ["viewer"]
      parameters:
        - name: orgID
          in: path
          required: true
          schema:
            type: integer
            format: int32
      responses:
        "200":
          description: List of users
```

### Implementation Example

```go
type MyValidator struct {
    db     *sql.DB
    logger *log.Logger
}

func (v *MyValidator) AuthFunc(c *fiber.Ctx) error {
    token := c.Get("Authorization")
    if token == "" {
        return errors.New("missing authorization header")
    }
    
    // Validate JWT token
    claims, err := validateJWT(token)
    if err != nil {
        return fmt.Errorf("invalid token: %w", err)
    }
    
    // Store user info in context
    c.Locals("userID", claims.UserID)
    c.Locals("userRole", claims.Role)
    
    return nil
}

func (v *MyValidator) OperationPermit(c *fiber.Ctx, operationID string) error {
    userRole := c.Locals("userRole").(string)
    
    // Define operation permissions
    permissions := map[string][]string{
        "ListOrgUsers":   {"viewer", "editor", "admin"},
        "CreateOrgUser":  {"editor", "admin"},
        "DeleteOrgUser":  {"admin"},
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
    
    return fmt.Errorf("insufficient permissions for operation %s", operationID)
}

func (v *MyValidator) ValidateOrgAccess(c *fiber.Ctx, orgID int32, requiredRole string) error {
    userID := c.Locals("userID").(int)
    
    // Check if user has access to the organization
    var userRole string
    err := v.db.QueryRow(
        "SELECT role FROM org_members WHERE user_id = $1 AND org_id = $2",
        userID, orgID,
    ).Scan(&userRole)
    
    if err == sql.ErrNoRows {
        return errors.New("user does not have access to this organization")
    }
    if err != nil {
        return fmt.Errorf("database error: %w", err)
    }
    
    // Check if user has required role
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

func (v *MyValidator) PreValidate(c *fiber.Ctx) error {
    // Global pre-validation logic
    return nil
}

func (v *MyValidator) PostValidate(c *fiber.Ctx) error {
    // Global post-validation logic
    return nil
}
```

## x-functions

`x-functions` define utility functions that can be used within your application logic. These are typically helper functions that extract or compute values from the request context.

### Structure

```yaml
x-functions:
  FunctionName:
    description: "Description of what this function does"
    useContext: true|false
    params:
      - name: parameterName
        description: "Parameter description"
        schema:
          type: string|integer|boolean|object
    return:
      name: returnValueName
      description: "Description of return value"
      schema:
        type: string|integer|boolean|object
```

### Properties

- **description**: Human-readable description of the function
- **useContext**: Whether the function needs access to the Fiber context
- **params**: Array of parameters the function accepts (optional)
- **return**: Definition of the return value

### Example Definition

```yaml
x-functions:
  GetOrgID:
    description: Get the organization ID from the request context
    useContext: true
    return:
      name: orgID
      description: The organization ID
      schema:
        type: integer
        format: int32
  
  GetUserID:
    description: Get the current user ID from the JWT token
    useContext: true
    return:
      name: userID
      description: The authenticated user ID
      schema:
        type: integer
        format: int32
  
  ComputeAccessLevel:
    description: Compute access level based on user role and resource type
    useContext: true
    params:
      - name: resourceType
        description: The type of resource being accessed
        schema:
          type: string
          enum: ["document", "project", "organization"]
    return:
      name: accessLevel
      description: The computed access level
      schema:
        type: string
        enum: ["read", "write", "admin"]
```

### Generated Interface

Functions are added to the `Validator` interface:

```go
type Validator interface {
    // ... other methods ...
    
    // Generated from x-functions
    GetOrgID(c *fiber.Ctx) (int32, error)
    GetUserID(c *fiber.Ctx) (int32, error)
    ComputeAccessLevel(c *fiber.Ctx, resourceType string) (string, error)
}
```

### Implementation Example

```go
func (v *MyValidator) GetOrgID(c *fiber.Ctx) (int32, error) {
    // Try to get from path parameters first
    if orgIDStr := c.Params("orgID"); orgIDStr != "" {
        orgID, err := strconv.ParseInt(orgIDStr, 10, 32)
        if err != nil {
            return 0, fmt.Errorf("invalid orgID in path: %w", err)
        }
        return int32(orgID), nil
    }
    
    // Try to get from query parameters
    if orgIDStr := c.Query("orgID"); orgIDStr != "" {
        orgID, err := strconv.ParseInt(orgIDStr, 10, 32)
        if err != nil {
            return 0, fmt.Errorf("invalid orgID in query: %w", err)
        }
        return int32(orgID), nil
    }
    
    // Try to get from request body
    var body struct {
        OrgID int32 `json:"orgID"`
    }
    if err := c.BodyParser(&body); err == nil && body.OrgID != 0 {
        return body.OrgID, nil
    }
    
    return 0, errors.New("orgID not found in request")
}

func (v *MyValidator) GetUserID(c *fiber.Ctx) (int32, error) {
    userID, ok := c.Locals("userID").(int)
    if !ok {
        return 0, errors.New("user ID not found in context")
    }
    return int32(userID), nil
}

func (v *MyValidator) ComputeAccessLevel(c *fiber.Ctx, resourceType string) (string, error) {
    userRole := c.Locals("userRole").(string)
    
    // Define access levels based on role and resource type
    accessMatrix := map[string]map[string]string{
        "document": {
            "viewer": "read",
            "editor": "write",
            "admin":  "admin",
        },
        "project": {
            "viewer": "read",
            "editor": "write",
            "admin":  "admin",
        },
        "organization": {
            "viewer": "read",
            "editor": "read",
            "admin":  "admin",
        },
    }
    
    resourceAccess, exists := accessMatrix[resourceType]
    if !exists {
        return "", fmt.Errorf("unknown resource type: %s", resourceType)
    }
    
    accessLevel, exists := resourceAccess[userRole]
    if !exists {
        return "", fmt.Errorf("unknown user role: %s", userRole)
    }
    
    return accessLevel, nil
}
```

## How It Works

### Code Generation Process

1. **Parsing**: The Anchor code generator reads your `api/v1.yaml` file and extracts the `x-check-rules` and `x-functions` definitions.

2. **Interface Generation**: A `Validator` interface is generated with methods corresponding to your rules and functions.

3. **Middleware Generation**: Middleware code is generated that wraps your API handlers and calls the appropriate validator methods.

4. **Integration**: The generated middleware is automatically integrated into your Fiber server.

### Middleware Execution Flow

For each API request, the middleware executes in this order:

1. **AuthFunc()** - Authentication (if security is defined)
2. **PreValidate()** - Global pre-validation
3. **Check Rules** - Specific rules defined in security scopes
4. **PostValidate()** - Global post-validation
5. **Handler** - Your actual API handler

### Generated Middleware Example

Here's what the generated middleware looks like for an operation:

```go
func (x *XMiddleware) ListOrgUsers(c *fiber.Ctx, orgID int32) error {
    // Authentication
    if err := x.AuthFunc(c); err != nil {
        return c.Status(fiber.StatusUnauthorized).SendString(err.Error())
    }
    
    // Pre-validation
    if err := x.PreValidate(c); err != nil {
        return c.Status(fiber.StatusForbidden).SendString(err.Error())
    }
    
    // Check rules (from security scopes)
    if err := x.ValidateOrgAccess(c, orgID, "viewer"); err != nil {
        return c.Status(fiber.StatusForbidden).SendString(err.Error())
    }
    
    // Post-validation
    if err := x.PostValidate(c); err != nil {
        return c.Status(fiber.StatusForbidden).SendString(err.Error())
    }
    
    // Call actual handler
    return x.ServerInterface.ListOrgUsers(c, orgID)
}
```

## Best Practices

### x-check-rules

1. **Keep rules focused**: Each rule should have a single responsibility
2. **Use descriptive names**: Rule names should clearly indicate their purpose
3. **Document parameters**: Provide clear descriptions for all parameters
4. **Handle errors gracefully**: Return meaningful error messages
5. **Cache when possible**: Cache expensive operations like database queries

### x-functions

1. **Pure functions**: When possible, make functions pure (same input = same output)
2. **Error handling**: Always return meaningful errors
3. **Context usage**: Use context locals to share data between functions
4. **Performance**: Consider caching expensive computations
5. **Type safety**: Use proper Go types for parameters and return values

### General

1. **Testing**: Mock the Validator interface for unit testing
2. **Logging**: Add appropriate logging for debugging and monitoring
3. **Documentation**: Keep your YAML documentation up to date
4. **Versioning**: Consider versioning your rules and functions
5. **Security**: Always validate and sanitize inputs

## Integration Example

Here's a complete example showing how to integrate everything:

```go
// main.go
package main

import (
    "database/sql"
    "log"
    
    "github.com/gofiber/fiber/v2"
    "your-app/pkg/zgen/apigen"
)

type Server struct {
    db     *sql.DB
    logger *log.Logger
}

func (s *Server) GetCounter(c *fiber.Ctx) error {
    // Your handler implementation
    return c.JSON(fiber.Map{"count": 42})
}

// Implement the Validator interface
func (s *Server) AuthFunc(c *fiber.Ctx) error {
    // Authentication logic
    return nil
}

func (s *Server) OperationPermit(c *fiber.Ctx, operationID string) error {
    // Permission checking logic
    return nil
}

func (s *Server) GetOrgID(c *fiber.Ctx) (int32, error) {
    // Extract org ID from request
    return 1, nil
}

// ... implement other required methods

func main() {
    app := fiber.New()
    
    server := &Server{
        db:     connectDB(),
        logger: log.Default(),
    }
    
    // Register handlers with middleware
    apigen.RegisterHandlersWithOptions(
        app,
        apigen.NewXMiddleware(server, server),
        apigen.FiberServerOptions{},
    )
    
    app.Listen(":8080")
}
```

This middleware system provides a powerful way to handle cross-cutting concerns like authentication, authorization, and request validation in a declarative manner, while maintaining type safety and generating efficient Go code.