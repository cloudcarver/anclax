-- name: UpsertWorker :one
INSERT INTO anclax.workers (id, labels, status, last_heartbeat, applied_config_version)
VALUES ($1, $2, 'online', CURRENT_TIMESTAMP, $3)
ON CONFLICT (id)
DO UPDATE SET
    labels = EXCLUDED.labels,
    status = 'online',
    last_heartbeat = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: UpdateWorkerHeartbeat :one
UPDATE anclax.workers
SET last_heartbeat = CURRENT_TIMESTAMP,
    status = 'online',
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: MarkWorkerOffline :exec
UPDATE anclax.workers
SET status = 'offline', updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateWorkerAppliedConfigVersion :exec
UPDATE anclax.workers
SET
    applied_config_version = GREATEST(applied_config_version, sqlc.arg(applied_config_version)),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: CreateWorkerRuntimeConfig :one
INSERT INTO anclax.worker_runtime_configs (payload)
VALUES ($1)
RETURNING *;

-- name: GetLatestWorkerRuntimeConfig :one
SELECT * FROM anclax.worker_runtime_configs
ORDER BY version DESC
LIMIT 1;

-- name: GetWorkerRuntimeConfigByVersion :one
SELECT * FROM anclax.worker_runtime_configs
WHERE version = $1;

-- name: ListOnlineWorkerIDs :many
SELECT id
FROM anclax.workers
WHERE
    status = 'online'
    AND last_heartbeat >= sqlc.arg(heartbeat_cutoff);

-- name: ListLaggingAliveWorkers :many
SELECT id
FROM anclax.workers
WHERE
    status = 'online'
    AND last_heartbeat >= sqlc.arg(heartbeat_cutoff)
    AND applied_config_version < sqlc.arg(version);

-- name: NotifyWorkerRuntimeConfig :exec
SELECT pg_notify('anclax_worker_runtime_config', sqlc.arg(payload)::text);

-- name: NotifyWorkerRuntimeConfigAck :exec
SELECT pg_notify('anclax_worker_runtime_config_ack', sqlc.arg(payload)::text);
