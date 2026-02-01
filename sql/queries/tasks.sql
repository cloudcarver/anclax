-- name: ClaimTask :one
UPDATE anclax.tasks
SET
    locked_at = CURRENT_TIMESTAMP,
    worker_id = sqlc.arg(worker_id),
    attempts = attempts + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = (
    SELECT t.id FROM anclax.tasks t
    WHERE
        t.status = 'pending'
        AND (
            t.started_at IS NULL OR t.started_at < NOW()
        )
        AND (
            t.locked_at IS NULL OR t.locked_at < sqlc.arg(lock_expiry)
        )
        AND (
            sqlc.arg(has_labels)::bool = false
            OR t.attributes->'labels' IS NULL
            OR jsonb_array_length(t.attributes->'labels') = 0
            OR (t.attributes->'labels' ?| sqlc.arg(labels)::text[])
        )
    ORDER BY RANDOM()
    LIMIT 1
)
RETURNING *;

-- name: ClaimTaskByID :one
UPDATE anclax.tasks
SET
    locked_at = CURRENT_TIMESTAMP,
    worker_id = sqlc.arg(worker_id),
    attempts = attempts + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE
    id = sqlc.arg(id)
    AND status = 'pending'
    AND (
        started_at IS NULL OR started_at < NOW()
    )
    AND (
        locked_at IS NULL OR locked_at < sqlc.arg(lock_expiry)
    )
    AND (
        sqlc.arg(has_labels)::bool = false
        OR attributes->'labels' IS NULL
        OR jsonb_array_length(attributes->'labels') = 0
        OR (attributes->'labels' ?| sqlc.arg(labels)::text[])
    )
RETURNING *;

-- name: ListAllPendingTasks :many
SELECT * FROM anclax.tasks
WHERE
    status = 'pending'
    AND (
        started_at IS NULL OR started_at < NOW()
    );

-- name: UpdateTaskStatus :exec
UPDATE anclax.tasks
SET 
    status = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateTaskStatusByWorker :one
UPDATE anclax.tasks
SET
    status = $2,
    locked_at = NULL,
    worker_id = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND worker_id = $3
RETURNING id;

-- name: UpdateTask :exec
UPDATE anclax.tasks
SET attributes = $2, spec = $3, started_at = $4, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateTaskStartedAt :exec
UPDATE anclax.tasks
SET started_at = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateTaskStartedAtByWorker :one
UPDATE anclax.tasks
SET started_at = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND worker_id = $3
RETURNING id;

-- name: RefreshTaskLock :one
UPDATE anclax.tasks
SET locked_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND worker_id = $2
RETURNING id;

-- name: ReleaseTaskLockByWorker :one
UPDATE anclax.tasks
SET locked_at = NULL, worker_id = NULL, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND worker_id = $2
RETURNING id;

-- name: CreateTask :one
INSERT INTO anclax.tasks (attributes, spec, status, started_at, unique_tag)
VALUES ($1, $2, $3, $4, $5) ON CONFLICT (unique_tag) DO NOTHING RETURNING *;

-- name: GetTaskByUniqueTag :one
SELECT * FROM anclax.tasks
WHERE unique_tag = $1;

-- name: InsertEvent :one
INSERT INTO anclax.events (spec)
VALUES ($1)
RETURNING *;

-- name: GetTaskByID :one
SELECT * FROM anclax.tasks
WHERE id = $1;

-- name: IncrementAttempts :exec
UPDATE anclax.tasks
SET attempts = attempts + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: VerifyTaskOwnership :one
SELECT id FROM anclax.tasks
WHERE id = $1 AND worker_id = $2;
