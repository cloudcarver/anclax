# DST (Distributed System Test)

`lib/dst` provides a hybrid YAML spec that generates typed Go interfaces and scenario runners.

Spec version:
- `dst/hybrid/v1alpha1`

Example spec:
- `lib/dst/examples/minimal.yaml`

## CLI

Validate spec:

```bash
go run ./cmd/dst validate -f lib/dst/examples/minimal.yaml
```

Generate Go code:

```bash
go run ./cmd/dst gen -f lib/dst/examples/minimal.yaml -o /tmp/dst_gen.go
```

## What gets generated

- interface definitions from YAML (`Worker`, `TaskStore`, ...)
- `Actors` struct with typed actor instances (`worker1`, `worker2`, ...)
- `Init(ctx, initActors)` function
- per-scenario runner functions
- `RunAll(ctx, initActors)` helper

Users provide actor construction in `initActors`:

```go
actors, err := Init(ctx, func(ctx context.Context) (Actors, error) {
    return Actors{
        Worker1:   newWorker(...),
        Worker2:   newWorker(...),
        TaskStore: newTaskStore(...),
    }, nil
})
```
