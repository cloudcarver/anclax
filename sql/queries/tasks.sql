-- name: PullTask :one
SELECT * FROM anchor.tasks
WHERE 
    status = 'pending'
    AND (
        started_at IS NULL OR started_at < NOW()
    )
ORDER BY RANDOM()
FOR UPDATE SKIP LOCKED
LIMIT 1;

-- name: PullTaskByID :one
SELECT * FROM anchor.tasks
WHERE 
    status = 'pending'
    AND id = $1
    AND (
        started_at IS NULL OR started_at < NOW()
    )
ORDER BY created_at ASC
FOR UPDATE SKIP LOCKED;

-- name: ListAllPendingTasks :many
SELECT * FROM anchor.tasks
WHERE 
    status = 'pending'
    AND (
        started_at IS NULL OR started_at < NOW()
    )
FOR UPDATE SKIP LOCKED;

-- name: UpdateTaskStatus :exec
UPDATE anchor.tasks
SET 
    status = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateTask :exec
UPDATE anchor.tasks
SET attributes = $2, spec = $3, started_at = $4, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateTaskStartedAt :exec
UPDATE anchor.tasks
SET started_at = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: CreateTask :one
INSERT INTO anchor.tasks (attributes, spec, status, started_at, unique_tag)
VALUES ($1, $2, $3, $4, $5) ON CONFLICT (unique_tag) DO NOTHING RETURNING *;

-- name: InsertEvent :one
INSERT INTO anchor.events (spec)
VALUES ($1)
RETURNING *;

-- name: GetTaskByID :one
SELECT * FROM anchor.tasks
WHERE id = $1;

-- name: IncrementAttempts :exec
UPDATE anchor.tasks
SET attempts = attempts + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;
