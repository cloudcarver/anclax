# Async Task Testing for Production Readiness

## Goal
This document explains how async-task scheduling/runtime-config behavior is tested in Anclax, why each test layer exists, and how these layers combine to provide production confidence.

## Scope
The strategy covers:
- Strict priority lane behavior (`priority > 0`).
- Weighted normal-lane behavior (`priority == 0`, group wheel).
- Runtime config propagation (`LISTEN/NOTIFY`, monotonic version apply, ACK signaling).
- Lease/lock lifecycle and serial-key claim gating.
- Concurrency safety under high contention.

## Test Layers
### 1. Unit tests (deterministic business rules)
Primary files:
- `pkg/taskcore/worker/engine_test.go`
- `pkg/taskcore/worker/runtime_test.go`
- `pkg/taskcore/worker/model_port_test.go`
- `pkg/taskcore/store/store_test.go`
- `pkg/taskcore/store/wait_test.go`
- `pkg/taskcore/store/wait_additional_test.go`
- `pkg/asynctask/executor_test.go`
- `pkg/asynctask/executor_additional_test.go`

What these prove:
- Strict-cap math boundaries and clamping behavior.
- Strict claim admission and fallback to normal claim groups.
- Weighted group probing order and wheel construction invariants.
- Runtime config apply/refresh behavior for stale/current/new versions.
- Payload validation rules for runtime-config task parameters.
- Failure reporting paths and fallback error rendering in task waiting.

### 2. Smoke tests (real DB + worker integration)
Primary files:
- `pkg/taskcore/e2e/dst_e2e_smoke_test.go` (requires Docker)
- `pkg/taskcore/e2e/dst_smoke_support_test.go`

What these prove:
- Claim/refresh/complete lease lifecycle over real Postgres queries.
- Worker loop lock refresh while handler is blocked.
- Serial-key gating and progression behavior across state transitions.
- Priority lane ordering and normal weighted group claim semantics.

### 3. Race tests (`-race` only)
Primary coverage:
- `pkg/taskcore/worker` engine/runtime tests executed under `-race`

What this proves:
- Concurrent runtime config updates and reads do not violate core invariants.
- Concurrent strict slot reserve/release activity is race-safe under detector instrumentation.

### 4. Fuzz tests (parser and normalization robustness)
Primary files:
- `pkg/asynctask/executor_fuzz_test.go`
- `pkg/taskcore/store/overrides_fuzz_test.go`

What these prove:
- Notification/payload parsing and normalization are robust to malformed inputs.
- Strict-cap math and wheel construction invariants hold across broad randomized input ranges.
- Override validation behavior remains stable under random inputs.

## Why This Is Production-Ready Confidence
The confidence comes from combined coverage across failure modes:
- Determinism: unit tests lock down exact boundary and branch behavior.
- Real integration: smoke tests validate SQL + worker orchestration semantics end-to-end.
- Concurrency safety: race suites exercise lock-protected shared state under load.
- Input hardening: fuzz suites explore malformed/edge payload spaces beyond hand-written cases.

No single test type is sufficient; the layered set addresses correctness, integration, and concurrency risk together.

## How to Run
Fast deterministic suites:
```bash
go test ./pkg/taskcore/worker ./pkg/asynctask ./pkg/taskcore
```

Race detector suites:
```bash
go test -race ./pkg/taskcore/worker ./pkg/asynctask
```

Smoke suites (Docker required):
```bash
go test -tags smoke ./pkg/taskcore/e2e -count=1 -v
```

Example short fuzz runs:
```bash
GOCACHE=/tmp/go-cache go test ./pkg/taskcore/store -run=^$ -fuzz=FuzzWithPriorityAndWithWeight -fuzztime=5s
GOCACHE=/tmp/go-cache go test ./pkg/asynctask -run=^$ -fuzz=FuzzBuildLabelWeights -fuzztime=5s
```

## Residual Risks and Operational Guardrails
Still true after this suite:
- Fairness is local-per-worker and therefore cluster-wide fairness is approximate.
- Runtime behavior depends on deployment health (DB latency, network stability, worker churn).

Recommended operational controls:
- Use the runtime-config and strict-lane metrics already emitted by worker/executor paths.
- Keep dashboards/alerts for saturation and convergence lag aligned with SLOs.
