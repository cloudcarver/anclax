# Distributed System Test (DST) Implementation Plan

## Goal
Provide a general distributed-system test spec that is:
- human-readable in YAML
- machine-validated
- compiled into typed Go code (interfaces + scenario runners)

Implementation root:
- `lib/dst`
- `cmd/dst/main.go`

---

## User-facing model

Single spec mode:
- `dst/hybrid/v1alpha1`

Design principles:
1. Users define actor interfaces in YAML.
2. Users map actor instances to interfaces.
3. Users define step barriers with per-actor call expressions.
4. Generator produces Go runners and typed contracts.
5. Users implement actor construction via `Init(ctx, initActors)`.

---

## YAML shape

```yaml
version: dst/hybrid/v1alpha1
package: dstgen

interfaces:
  Worker:
    methods:
      - Claim(ctx context.Context) error
  TaskStore:
    methods:
      - Enqueue(ctx context.Context, task string) error

instances:
  worker1: Worker
  worker2: Worker
  taskStore: TaskStore

scenarios:
  - name: basic
    steps:
      - id: s1
        parallel:
          taskStore:
            - Enqueue(ctx, "S1")
      - id: s2
        parallel:
          worker1:
            - Claim(ctx)
          worker2:
            - Claim(ctx)
```

---

## Barrier semantics

For each step:
- each actor cell runs concurrently
- calls inside one actor cell run sequentially
- next step starts only after all actor cells complete

This preserves deterministic step ordering while allowing concurrent actor behavior.

---

## Components

### Parsing
- `load.go`
- `parse.go`

### Validation
- `validate.go`

Checks include:
- version compatibility
- interface/method declarations
- instance-to-interface mapping
- scenario/step uniqueness
- call expression method existence and argument count

### Generation
- `gen.go`

Generated output includes:
- interface types
- `Actors` struct
- `Init(...)` and `ValidateActors(...)`
- per-step/per-scenario runner functions
- `RunAll(...)`

### CLI
- `dst validate`
- `dst gen`

---

## Milestones

### M1 (done)
- hybrid parser/validator
- generator
- CLI validate/gen
- minimal examples + tests

### M2
- richer call-expression typing checks (not just arg count)
- better generator diagnostics with source location

### M3
- optional generator hooks (`BeforeStep`, `AfterStep`, tracing)
- optional scenario filtering in generated helpers

### M4
- test harness adapters for chaos/fault orchestration
