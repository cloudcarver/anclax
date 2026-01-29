# CRUD Operations Example in Anclax

The API spec is the single source of truth. All code are implemented by depending on it.
```yaml
# example: v1.yaml
paths:
  /help:
    get:
      operationId: getHelp # the method name
      requestBody:
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/GetHelpReq' # always use schema reference, NO inline schema
      responses:
        '200':
          description: successful operation
          content:
            application/json:
              schema:
                type: array # array is allowed here, but the exact schema of the item must be referenced
                items:
                  $ref: '#/components/schemas/HelpItem' # always use schema reference
components:
  schema:
    GetHelpReq:
      type: object
      properties:
        query:
          type: string
    HelpItem:
      type: object
      properties:
        id:
          type: string
        content:
          type: string
```

The interface of handler or controller are generated from the API spec, it translates the service layer to HTTP APIs.
```go
// example: handlers.go
func (h *Handler) GetHelp(c *fiber.Ctx) error {
    var req apigen.GetHelpReq // use the generated type
    if err := c.BodyParser(&req); err != nil {
        return c.Status(fiber.StatusBadRequest).SendString(err.Error())
    }
    items, err := h.svc.GetHelp(c.Context(), req)
    if err != nil {
        if errors.Is(err, service.ErrHelpNotFound) {
            return c.JSON([]apigen.HelpItem{}) // translate service error to HTTP response or HTTP error code
        }
        return err
    }
    return c.JSON(items)
}
``` 

The service layer implements the business logic, it translates the model types to types generated from the API spec.  
```go
var ErrHelpNotFound = errors.New("help items not found")

func (s *Service) GetHelp(ctx context.Context, req apigen.GetHelpReq) ([]*apigen.HelpItem, error) {
    items, err := s.model.MatchHelps(ctx, req.Query) // use the model layer to get data
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return nil, ErrHelpNotFound // translate model error to service error
        }
        return nil, err // the unexpected error is directly returned and will be handled by the global error handler
    }
    return utils.ToSlice(items, helpToSpec), nil
}

// translate model type to API spec type, should not have error to make it compatible with ToSlice generic function.
func helpToSpec(h *querier.Help) *apigen.HelpItem {
    return &apigen.HelpItem{
        ID:      h.ID.String(),
        Content: h.Content,
    }
}
```

The model layer interacts with the database, its types and interfaces are generated via sqlc. 
Define schema in `sql/migrations`, make sure write a new migration step when changing the database schema. If these changes are in the same commit, just use one new migration file. 
Define queries in `sql/queries`, sqlc will generate types and interfaces for these queries. 

```sql
-- name: MatchHelps :many
SELECT * FROM helps WHERE content ILIKE '%' || $1 || '%';
```

Check `sqlc.yaml` to see where the generated code is located.
