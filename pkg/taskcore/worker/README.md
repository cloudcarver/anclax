# worker

`worker` is the task worker package designed for deterministic distributed-system testing.

## Architecture

- `Engine`: pure state machine.
  - Input: `Event`
  - Output: `[]Command`
- `Runtime`: trigger shell and side-effect executor.
  - Drives periodic events (`poll`, `heartbeat`, `runtime config poll`)
  - Executes commands through `Port`
- `Port`: side-effect boundary (DB/queue/handler/runtime-config APIs).
- `Worker`: production facade compatible with the task handler interface:
  - `NewWorker(globalCtx, cfg, model, taskHandler)`
  - `Start()`
  - `RunTask(ctx, taskID)`
  - `RegisterTaskHandler(handler)`

## Why this split

The old worker is loop-driven and timing-driven. This package makes phase boundaries explicit so tests can serialize interleavings across workers.

A single cycle is broken into independent steps:

1. `claim_strict` or `claim_normal`
2. `execute_task`
3. `finalize`

Each step is an explicit command emitted by the engine and can be ordered deterministically in tests.

## Deterministic testing model

Use `Engine.Apply(event)` directly in tests to serialize event order across workers.

Use `Runtime.Step(ctx, event)` when you want to include side-effect adapters (`Port`) while keeping deterministic event ordering.

For production usage, `NewWorker(...)` wires:
- `Engine` + `Runtime`
- model-backed `Port` (`ModelPort`)
- worker lifecycle handler semantics (retry/cron/failure logic)
- optional runtime-config LISTEN loop when DSN is configured.
