# Database

Schemas live in `sql/migrations`.
Queries live in `sql/queries`.

These two directories are the source of truth. Run `anclax gen` after modifications to regenerate `pkg/zgen/querier`.

Follow these rules when defining database schema and queries:
1. Define JSONB column schema in `api/v1.yaml` and let `anclax gen` generate Go types in `pkg/zgen/apigen`. Then map the type in `sqlc.yaml`.
2. For required fields, use `NOT NULL`. Nullable columns become pointer types in Go.
3. If an ID is publicly exposed, use UUID.
4. Add indexes based on query patterns.
5. Queries define the building blocks; assemble them in the service layer.

The generated code is wrapped by `pkg/zcore/model`. 
To run a transaction, use these based on the use case:
```go
// The callback is in tx, return error to rollback, return nil to commit.
RunTransactionWithTx(ctx context.Context, f func(tx core.Tx, model ModelInterface) error) (retErr error)
RunTransaction(ctx context.Context, f func(model ModelInterface) error) error
// Usually in another method where tx is already created, these methods accepts tx in params. 
SpawnWithTx(tx core.Tx) ModelInterface 
```
