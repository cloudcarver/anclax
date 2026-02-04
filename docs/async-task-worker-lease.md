# Async Task Worker Lease Design

This document describes the worker lease model for async tasks.

## Goals

- Avoid long-lived database transactions during task execution.
- Use lease-based task claiming with `locked_at` and `worker_id`.
- Refresh locks while a task runs; reclaim tasks when locks expire.
- Keep worker heartbeats for monitoring without join-heavy claim queries.
- Allow workers to filter tasks by labels.

## Schema changes

Tasks:
- `locked_at TIMESTAMPTZ` (lease timestamp)
- `worker_id UUID` (current owner)

Workers:
- `id UUID` (primary key)
- `labels JSONB` (array of strings)
- `status TEXT` (online/offline)
- `last_heartbeat TIMESTAMPTZ`
- `created_at`, `updated_at`

## Claim and lease

1) Claim task in a short transaction:
- `status = 'pending'`
- `started_at IS NULL OR started_at < now()`
- `locked_at IS NULL OR locked_at < now() - lock_ttl`
- Labels match or task has no labels

2) Update row:
- `locked_at = now()`
- `worker_id = <worker>`
- `attempts = attempts + 1`

3) Commit immediately, then execute task outside the transaction.

4) Refresh `locked_at` on an interval while executing.

5) On completion/failure, update status and release lock in a short transaction.

## Heartbeat

Workers upsert a registry row at startup and update `last_heartbeat` on a ticker.
Heartbeat is for monitoring only; task claims use `locked_at` TTL only.

## Labels

- Task labels come from `api/tasks.yaml` and runtime overrides.
- Worker labels come from config.
- If a worker has labels, it can claim tasks with intersecting labels or no labels.
- If a worker has no labels, it can claim all tasks.

## Execution flow

1) Claim task (short tx).
2) Handle cron scheduling (short tx) if needed.
3) Execute task without DB transaction.
4) On success/failure, update task state and release lock (short tx).

## Config

- `worker.pollinterval`
- `worker.concurrency`
- `worker.heartbeatInterval`
- `worker.lockTtl`
- `worker.lockRefreshInterval`
- `worker.labels`
- `worker.workerId` (optional)

## Test plan (mock-based)

Regression:
- Claim path uses short tx only and does not run executor in tx.
- Status updates and events still occur for success/failure paths.
- Retry scheduling still updates `started_at` and respects policy.

New feature tests:
- Claim filters by lock TTL and labels.
- Lock refresh updates `locked_at` while running.
- Ownership guard prevents stale workers from updating status.
- Heartbeat registration and periodic updates.

## Smoke test (docker)

Runs a Postgres-backed regression test that validates claim, refresh, and release flow with labels.
Requires Docker and uses port 5499.

```bash
go test -tags=smoke ./pkg/taskcore
```

Make target:
```bash
make smoke
```
