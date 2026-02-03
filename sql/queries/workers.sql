-- name: UpsertWorker :one
INSERT INTO anclax.workers (id, labels, status, last_heartbeat)
VALUES ($1, $2, 'online', CURRENT_TIMESTAMP)
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
