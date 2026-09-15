-- name: UpsertWorker :one
INSERT INTO anclax.workers (id, labels, status, last_heartbeat, applied_config_version)
VALUES ($1, $2, 'online', CURRENT_TIMESTAMP, $3)
ON CONFLICT (id)
DO UPDATE SET
    labels = EXCLUDED.labels,
    applied_config_version = GREATEST(anclax.workers.applied_config_version, EXCLUDED.applied_config_version),
    status = 'online',
    last_heartbeat = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: UpdateWorkerHeartbeat :one
UPDATE anclax.workers
SET last_heartbeat = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status = 'online'
RETURNING *;

-- name: MarkWorkerOffline :exec
UPDATE anclax.workers
SET status = 'offline', updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateWorkerAppliedConfigVersion :exec
UPDATE anclax.workers
SET
    applied_config_version = GREATEST(applied_config_version, sqlc.arg(applied_config_version)),
    prefetch_strict_percentage = COALESCE((SELECT (payload->>'maxStrictPercentage')::int
        FROM anclax.worker_runtime_configs ORDER BY version DESC LIMIT 1),prefetch_strict_percentage),
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

-- name: CreateWorkerRuntimeConfigForRequest :one
INSERT INTO anclax.worker_runtime_configs (request_id, payload)
VALUES ($1, $2)
ON CONFLICT (request_id) DO UPDATE SET request_id = EXCLUDED.request_id
RETURNING *;
