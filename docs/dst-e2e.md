# DST End-to-End (E2E) Testing

## Overview
DST (Distributed System Test) lets you describe end-to-end scenarios in YAML and generate strongly-typed Go runners. Each scenario is a sequence of steps; within a step, actor calls run in parallel and the step waits for all calls to finish.

This doc shows how to define a DST spec, generate code, and wire it into E2E tests.

## 1) Write a DST spec (`e2e.yaml`)
Use the hybrid spec version `dst/hybrid/v1alpha1`.

Key parts:
- `interfaces`: method signatures your actors must implement.
- `instances`: named actor instances used in scenarios.
- `scenarios`: ordered steps; each step contains a `parallel` map keyed by actor name.

Example (trimmed from `e2e.yaml`):

```yaml
version: dst/hybrid/v1alpha1
package: taskcoree2e

interfaces:
  Worker:
    methods:
      - Claim(ctx context.Context) error
      - CompleteLast(ctx context.Context) error
  TaskStore:
    methods:
      - Enqueue(ctx context.Context, task string, priority int32, weight int32, labels []string) error

instances:
  worker1: Worker
  worker2: Worker
  taskStore: TaskStore

scenarios:
  - name: strict_priority_and_weighted_groups
    description: strict tasks claim by priority; normal grouped claim uses weight order.
    steps:
      - id: s1
        parallel:
          taskStore:
            - Enqueue(ctx, "S_LOW", 1, 1, []string{})
            - Enqueue(ctx, "S_HIGH", 9, 1, []string{})
      - id: s2
        parallel:
          worker1:
            - Claim(ctx)
            - CompleteLast(ctx)
```

Notes:
- `steps` are sequential; actors inside each `parallel` block run concurrently.
- Calls must match methods defined under `interfaces`.

### Script steps (hybrid Go)
You can embed Go snippets in a step using `script`. Script steps run sequentially and cannot be combined with `parallel` in the same step.

The script has access to:
- `ctx` (context)
- `actors` (all actor instances)
- `set(name, value)` / `get(name)` for cross-step variables
- `t` for `require.*` assertions (e.g., `require.NoError(t, err)`)

Example:
```yaml
  - id: s3
    script: |
      taskAId, err := actors.TaskStore.Enqueue(ctx, "A", 1, 1, nil)
      require.NoError(t, err)
      set("taskAId", taskAId)
```

## 2) Generate code

### Option A: via `anclax gen` (recommended)
Add a `dst` section to `anclax.yaml`:

```yaml
# anclax.yaml
# DST generation is built into `anclax gen`.
dst:
  - path: e2e.yaml
    out: pkg/taskcore/e2e/gen/taskstore_gen.go
    package: taskcoree2e
```

Run:

```bash
anclax gen
```

### Option B: direct DST CLI

```bash
go run ./cmd/dst validate -f e2e.yaml
go run ./cmd/dst gen -f e2e.yaml -o pkg/taskcore/e2e/gen/taskstore_gen.go -pkg taskcoree2e
```

## 3) Implement actors and run scenarios
The generated file provides:
- interfaces for each actor (`Worker`, `TaskStore`, ...)
- `Actors` struct (named fields match `instances` keys)
- per-scenario runner + `RunAll`

Example usage (see `pkg/taskcore/dst_e2e_smoke_test.go`):

```go
err := taskcoree2e.RunAll(ctx, func(ctx context.Context) (taskcoree2e.Actors, error) {
    return taskcoree2e.Actors{
        TaskStore: env.taskStore,
        Worker1:   env.worker1,
        Worker2:   env.worker2,
    }, nil
})
```

Your actor types should implement the generated interfaces (methods listed in the YAML).

## 4) Run the E2E suite
The taskcore E2E tests use the `smoke` build tag:

```bash
go test -tags smoke ./pkg/taskcore -count=1 -v
```

## Tips
- Keep step IDs stable (`s1`, `s2`, …) for easier diffs.
- Prefer small, focused scenarios to keep failures localized.
- Use `parallel` blocks for true concurrency; separate steps for ordered sequences.
