# Database

schemas -> `sql/migrations`
queries -> `sql/queries`

These 2 files are single source of truth. Use `anclax gen` after modification and code will be generated into `pkg/zgen/querier`.

Follow these rules when defining database schema and queries:
1. Define JSONB column schema in `api/v1.yaml` and let `anclax gen` generate the Go type in `pkg/zgen/apigen`. Then define the type mapping in `sqlc.yaml`. 
2. For required field, use NOT NULL. NULLABLE column will be a pointer type in Go. 
3. If the ID is publicly exposed, use UUID type. 
4. Use proper indexing according to query patterns.
5. Queries define the bulding blocks, assemble them in service layer.

The generated code is wrapped by `pkg/zcore/model`. 
To run a transaction, use these according to the use case:
```go
// The callback is in tx, return error to rollback, return nil to commit.
RunTransactionWithTx(ctx context.Context, f func(tx core.Tx, model ModelInterface) error) (retErr error)
RunTransaction(ctx context.Context, f func(model ModelInterface) error) error
// Usually in another method where tx is already created, these methods accepts tx in params. 
SpawnWithTx(tx core.Tx) ModelInterface 
```
