# Async Task Scheduling: Priority, Weight, and Runtime Worker Config

English | [中文](async-task-scheduling-runtime-config.zh.md)

This guide explains how Anclax schedules async tasks with strict priority + weighted fairness, and how to update worker scheduling config at runtime without restarting workers.

## Table of Contents

- [Quick Summary](#quick-summary)
- [Task Classes and Selection Order](#task-classes-and-selection-order)
- [Using `WithPriority` and `WithWeight`](#using-withpriority-and-withweight)
- [Runtime Worker Config Task](#runtime-worker-config-task)
- [Propagation Flow (LISTEN/NOTIFY + DB)](#propagation-flow-listennotify--db)
- [Operational Notes](#operational-notes)
- [References](#references)

## Quick Summary

- `priority > 0` => **strict lane** (urgent lane).
- `priority == 0` => **normal lane** (weighted fairness lane).
- Strict lane admission is capped by `maxStrictPercentage` against worker concurrency.
- Normal lane fairness is controlled by runtime label-group weights (`labelWeights`).
- Within a selected normal group, tasks are ordered by `weight DESC`, then FIFO fields (`created_at`, `id`).
- Runtime config updates are versioned in DB and propagated via Postgres `LISTEN/NOTIFY` + worker ACK state.

## Task Classes and Selection Order

### 1) Strict lane (`priority > 0`)

- Strict tasks are attempted first.
- Order in strict lane:
  1. `priority DESC`
  2. `created_at ASC`
  3. `id ASC`
- Strict tasks can only be claimed when `strict_inflight < strict_cap`.

Strict cap formula:

```text
strict_cap = ceil(concurrency * maxStrictPercentage / 100)
```

Clamped behavior:
- `maxStrictPercentage <= 0` => `strict_cap = 0`
- `maxStrictPercentage >= 100` => `strict_cap = concurrency`

### 2) Normal lane (`priority == 0`)

- Used when strict slot is unavailable or no strict task is claimable.
- Worker rotates claim groups using a weighted wheel built from runtime `labelWeights`.
- Every normal task maps to one group:
  - If task labels intersect configured weighted labels: deterministic label = lexicographically smallest match.
  - Otherwise: `default` group.
- Group selection fairness is per-worker (cluster-wide fairness is approximate).

Within the selected group, SQL claim order is:
1. `weight DESC`
2. `created_at ASC`
3. `id ASC`

## Using `WithPriority` and `WithWeight`

`taskcore` overrides:

- `taskcore.WithPriority(priority int32)`
  - validation: `priority >= 0`
- `taskcore.WithWeight(weight int32)`
  - validation: `weight >= 1`

Example:

```go
taskID, err := taskRunner.RunSendWelcomeEmail(ctx, params,
    taskcore.WithPriority(10), // strict lane, urgent task
    taskcore.WithWeight(3),    // within chosen normal group, higher first
)
```

Suggested usage:
- Use `WithPriority` only for rare urgent traffic (incidents, administrative remediation, internal control tasks).
- Keep most business tasks at `priority=0`, tune fairness with runtime `labelWeights`, and optionally per-task `weight` for within-group ordering.

## Runtime Worker Config Task

Anclax includes task `updateWorkerRuntimeConfig` (`api/tasks.yaml`) to update scheduling config at runtime.

Generated params (`taskgen.UpdateWorkerRuntimeConfigParameters`):

- `requestID` (optional): correlation ID; auto-generated if empty.
- `maxStrictPercentage` (optional): integer in `[0, 100]`.
- `defaultWeight` (optional): integer `>= 1`.
- `labels` + `weights` (optional): arrays with same length.
- `notifyInterval` (optional): e.g. `"1s"`; must be positive.
- `listenTimeout` (optional): e.g. `"2s"`; must be positive.

Note: the control-plane API supplies `requestID`, `notifyInterval`, and `listenTimeout`; callers should not set them directly.

### Use the worker control plane

Use the control plane API to enqueue and wait for runtime config updates:

```go
import "github.com/cloudcarver/anclax/pkg/taskcore/ctrl"

maxStrict := int32(20)
defaultWeight := int32(1)
labels := []string{"w1", "w2"}
weights := []int32{5, 1}

controlPlane := ctrl.NewWorkerControlPlane(model, taskRunner, taskStore)
err := controlPlane.UpdateWorkerRuntimeConfig(ctx,
    &ctrl.UpdateWorkerRuntimeConfigRequest{
        MaxStrictPercentage: &maxStrict,
        DefaultWeight:       &defaultWeight,
        Labels:              labels,
        Weights:             weights,
    },
)
```

Why the control plane is required:
- It always applies reserved strict priority (`math.MaxInt32`) for config-update tasks.
- It prevents accidental lower-priority enqueueing of control-plane updates.
- It hides request IDs and LISTEN/NOTIFY tuning from callers; the control plane handles retries and ACK waits.

## Propagation Flow (LISTEN/NOTIFY + DB)

### Write side (config-update task executor)

1. Validate and normalize payload.
2. Insert versioned row into `anclax.worker_runtime_configs`.
3. Notify channel `anclax_worker_runtime_config` with:
   - `op: "up_config"`
   - `{request_id, version}`
4. Loop until converged or superseded:
   - query alive lagging workers (`applied_config_version < target_version`)
   - re-notify
   - optionally wait on ACK channel `anclax_worker_runtime_config_ack`

### Worker side

1. Start runtime config loop (`LISTEN` first, then refresh latest from DB).
2. On notify (or fallback poll), fetch latest config version.
3. If newer, apply in-memory atomically and update worker row via monotonic `GREATEST` write.
4. Emit ACK notify with `{request_id, worker_id, applied_version}`.

### Convergence truth source

- Notification/ACK accelerate wakeups.
- **DB state is authoritative** for completion:
  - converged when no alive worker is lagging target version.
- If a newer version appears while waiting, the older update is treated as superseded and exits early.

## Operational Notes

- Startup/default strict cap can come from `worker.maxStrictPercentage` in app config.
- Runtime updates can override scheduling behavior without restarting workers.
- Optional fallback polling (`worker.runtimeConfigPollInterval`) helps when notifications are unavailable.
- Fairness is local-per-worker; validate SLO behavior with production traffic patterns.

Useful metrics:
- `anclax_worker_strict_inflight`
- `anclax_worker_strict_cap`
- `anclax_worker_strict_saturation_total`
- `anclax_worker_runtime_config_version`
- `anclax_runtime_config_lagging_workers`
- `anclax_runtime_config_convergence_seconds`
- `anclax_runtime_config_superseded_total`

## References

- Overview and architecture: [async-tasks-technical.md](async-tasks-technical.md)
- Hands-on guide: [async-tasks-tutorial.md](async-tasks-tutorial.md)
- Worker lease model: [async-task-worker-lease.md](async-task-worker-lease.md)
- Test strategy and production-readiness confidence: [async-task-testing-production-readiness.md](async-task-testing-production-readiness.md)
