# taskcore chaos

Containerized longevity + chaos harness for taskcore.

This package is the start of a higher-fidelity chaos system test for taskcore.
Unlike the in-process smoke harness under `pkg/taskcore/e2e`, this package runs the main roles in **separate docker containers** so we can exercise real process loss and rejoin behavior.

## Why this package exists

The existing E2E smoke tests are useful, but they still model some failures in-process.
This package is intended for cases where maintainers want to verify behavior closer to production reality:

- Postgres is a separate container
- control plane is a separate container
- each worker is a separate container
- the orchestrator/test runner stays outside the chaos containers
- chaos is applied by stopping/restarting/removing containers

That makes it much better suited for:

- worker disappears for a long time, then comes back
- worker disappears forever and is replaced later
- control plane is unavailable while workers/database are still alive
- Postgres restarts while work is in flight
- backlog drains only after enough capacity returns

## Current architecture

The package currently has five main parts:

### 1. Harness / orchestrator
Files:

- `harness.go`
- `docker.go`
- `config.go`
- `report.go`
- `binaries.go`

Responsibilities:

- create a dedicated docker network per run
- assign a run ID
- build helper binaries
- start/stop/restart containers
- start a host-side user signal service outside the chaos containers
- expose a control-plane HTTP client
- expose a user signal client
- expose a DB inspector
- collect artifacts and write `report.json`

### 2. Helper binaries
Files:

- `cmd/controlplane/main.go`
- `cmd/worker/main.go`

These binaries are built on the host at test startup and bind-mounted into lightweight runtime containers.

#### controlplane helper
A very small HTTP service used only by the chaos harness.
It currently supports:

- `GET /healthz`
- `POST /tasks/stress-probe`
- `POST /tasks/pause`
- `POST /tasks/cancel`
- `POST /tasks/resume`
- `POST /runtime-config`

It talks directly to the real taskcore model/task store/control plane code.

#### worker helper
A dedicated worker process running in its own container.
It uses the real worker implementation plus the async-task executor and worker control task handler.

### 3. Host-side user signal service
Files:

- `signal_service.go`

This is a tiny in-memory HTTP service started by the harness on the host, outside the chaos containers.
Workers can emit task signals to it while executing, and the user/test runner can poll it to observe liveness.
It currently supports:

- `GET /healthz`
- `POST /signals/emit`
- `GET /signals/{taskID}`
- `DELETE /signals/{taskID}`

### 4. User-facing test API
Files:

- `controlplane_client.go`
- `user.go`
- `inspector.go`
- `signal_service.go`

The intent is to model tests as user expectations rather than only raw chaos events.
Currently the `User` helper can:

- submit stress-probe tasks via the control-plane container
- pause, cancel, and resume tasks by unique tag through the control-plane container
- wait for completed / cancelled / paused states through DB inspection
- wait for running-task signals and verify they stop after cancellation

### 5. First smoke scenario
File:

- `chaos_smoke_test.go`

This is the first real containerized scenario.
It is intentionally small, but already covers:

- worker down / rejoin
- worker retirement + replacement worker later
- control-plane down / up
- Postgres restart
- runtime config update through control plane
- user pause / resume / cancel operations through the control plane
- eventual completion / cancellation assertions through DB inspection

## Naming and run IDs

Every run gets a run ID.
Container and network names use the prefix:

- `anclax-longevity-<runID>-...`

Examples:

- `anclax-longevity-<runID>-postgres`
- `anclax-longevity-<runID>-control`
- `anclax-longevity-<runID>-worker-a`
- `anclax-longevity-<runID>-net`

This matters because it makes cleanup and debugging much easier when a run fails.

## Smoke test

```bash
go test -tags smoke ./pkg/taskcore/chaos -run TestContainerizedTaskcoreChaosSmoke -count=1 -v -timeout 15m
```

The test logs the artifact directory at the end of the run.

## Artifact layout

Each run writes artifacts into a unique directory, for example:

```text
/tmp/anclax-taskcore-chaos-<runID>-<suffix>/
```

Current contents:

- `report.json`
- `summary.txt`
- `bin/`
  - built helper binaries used for the run
- `docker/`
  - `docker inspect` output
  - `docker logs` output
- `db/`
  - task snapshot JSON

### `report.json`
This is the main replay/debug entry point.
It currently records:

- run ID
- seed
- start/end time
- artifact dir
- ordered event timeline
- end-of-run summary
- failure summary, if any

The summary is intended to give maintainers a quick health snapshot after the chaos run, for example:

- how many times each component went down
- how many times components restarted
- how many tasks were submitted / observed / processed
- how many tasks completed / remained pending / were retried
- how many worker disruptions, Postgres restarts, control-plane outages, and replacement workers occurred

Important event kinds include:

- harness/build events
- signal-service lifecycle
- docker network/container lifecycle
- user task submissions
- user signal assertions
- chaos actions
- runtime-config updates

When debugging a failure, start with `report.json` first.

## Current guarantees

The current smoke test is trying to verify these properties:

- tasks submitted while the control plane is up eventually become visible in the DB
- workers can disappear and later rejoin
- workers can be retired and replaced by new workers later
- Postgres restarts do not permanently wedge the system once capacity returns
- eventual convergence happens after the cluster is restored
- paused tasks can be resumed and eventually complete
- cancelled tasks remain cancelled
- cancellation of a running task can be observed by user-visible signal emission stopping
- when there has been disruption, at least some tasks should show takeover/retry evidence (`attempts >= 2`)

## Current limitations

Maintainers should be aware of the following limitations.

### 1. This is still an early foundation
The package is usable, but it is not yet a complete longevity lab.
The first smoke test is intentionally narrow.

### 2. The control-plane helper is not the production server binary
It is a dedicated test helper process that calls the real model/taskcore code, but it is not the full Anclax HTTP server.
That is good enough for the current taskcore scenarios, but not identical to full app deployment.

### 3. Current user scenarios are simple
Right now the main user workload is `stressProbe`.
That is useful for:

- occupancy
- retries/takeover
- backlog drain
- worker churn

But it does **not** yet validate richer semantics such as:

- idempotent side effects
- nested parent/child descendant behavior in the long soak
- business-level task payload validation

### 4. Chaos actions are currently container lifecycle oriented
Current actions focus on:

- stop/remove/restart containers

Not yet covered here:

- `docker pause` / `unpause`
- network disconnect/reconnect
- packet loss / latency / `tc netem`
- CPU or memory pressure
- connection-pool starvation

### 5. Final-state assertions are stronger than intermediate assertions
The current test is mostly an eventual-convergence check.
That means it may miss some transient bugs that self-heal before the final assertions.

## Important implementation details

### Helper binaries are built at test startup
The harness calls `go build` for:

- `./pkg/taskcore/chaos/cmd/controlplane`
- `./pkg/taskcore/chaos/cmd/worker`

These binaries are written into the run artifact directory and then bind-mounted into runtime containers.

This means maintainers should expect:

- first startup cost from `go build`
- helper binary changes to affect smoke runs immediately
- runtime containers to depend on the host-built binaries, not prebuilt images

### Runtime image choice
The current runtime image is a lightweight container image used only to host the mounted helper binaries.
The helper binaries are built with `CGO_ENABLED=0` so they can run there.

If you introduce dependencies that require cgo or non-static runtime support, you may need to revisit this design.

### DB inspection is host-side
The harness uses a host-side inspector against the published Postgres port.
That means:

- diagnostics still work even if control plane or workers are down
- artifact collection does not depend on worker/control availability

## Maintenance guidance

### When adding a new chaos action
Prefer the pattern already used in `chaos_smoke_test.go`:

1. mutate the container topology with the harness
2. update the in-memory scenario state
3. emit a report event with enough fields to replay the action later

At minimum, log:

- iteration
- target container / worker name
- whether the worker is expected to return
- when it is expected to return

### When adding a new user scenario
Prefer modeling it as a user-visible contract.
Example shape:

- user submits tasks via control plane
- chaos happens while tasks are in flight
- user expects terminal status or bounded recovery behavior
- DB inspector verifies the outcome

Try to avoid tests that only say “do random stuff and hope nothing fails”.
Scenarios should still have clear expectations.

### When adding new diagnostics
Put them where maintainers will naturally look:

- `report.json` for timeline / replay metadata
- `db/` for SQL-derived state
- `docker/` for container logs and inspect data

Good additions would be:

- worker registry snapshot
- runtime config snapshot
- counts by task status/type
- pending/running control-task snapshot
- last task error events

### When debugging a failure
Recommended order:

1. open `report.json`
2. find the first suspicious chaos event before failure
3. inspect DB snapshot under `db/`
4. inspect control-plane and worker logs under `docker/`
5. replay using the same seed / run settings

## Reproducibility

Current reproducibility is based on:

- run ID for locating artifacts and containers
- seed recorded in `report.json`
- ordered event log in `report.json`

This is already much better than a purely ad-hoc random test.
However, it is still possible to miss bugs or get partial replay drift if timing-sensitive races depend on host load.

## What maintainers should improve next

Recommended next steps for this package:

1. add richer user scenarios
   - pause/cancel
   - parent/child tasks
   - idempotent side effects
   - runtime config convergence assertions
2. add more diagnostics
   - worker table snapshot
   - control task snapshot
   - event snapshot
3. add more chaos types
   - pause/unpause
   - network partition
   - latency/loss injection
4. make some long runs configurable
   - iterations
   - number of workers
   - worker labels
   - task volume / sleep
5. consider a real app/control-plane process image later if full HTTP/API realism is needed

## Current scope summary

First version covers:

- dedicated docker network per run
- real Postgres container
- real separate control-plane helper container
- real separate worker helper containers
- host-side in-memory user signal service
- random worker down/rejoin
- worker retirement + replacement worker join later
- control-plane container down/up
- Postgres restart
- runtime config updates via control plane
- user-style task submission plus pause / resume / cancel operations
- observable running-task signals for cancel verification
- expected eventual completion / cancellation outcomes

## Quick file map

- `config.go`: harness run config and defaults
- `report.go`: structured run report
- `docker.go`: docker helpers
- `binaries.go`: build helper binaries for containers
- `harness.go`: orchestration API
- `controlplane_client.go`: HTTP client for helper control plane
- `signal_service.go`: host-side signal service and client
- `inspector.go`: DB inspection and diagnostic dumps
- `user.go`: user-facing submission/assertion helpers
- `chaos_smoke_test.go`: first containerized chaos test
- `cmd/controlplane/main.go`: helper control-plane process
- `cmd/worker/main.go`: helper worker process
