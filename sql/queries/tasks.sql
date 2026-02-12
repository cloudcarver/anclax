-- name: ClaimTask :one
WITH
    -- Eligible tasks are pending, due, unlocked, and label-compatible.
    eligible AS (
        SELECT t.*
        FROM anclax.tasks t
        WHERE
            t.status = 'pending'
            AND (t.started_at IS NULL OR t.started_at < NOW())
            AND (t.locked_at IS NULL OR t.locked_at < sqlc.arg(lock_expiry))
            AND (
                sqlc.arg(has_labels)::bool = false
                OR t.attributes->'labels' IS NULL
                OR jsonb_array_length(t.attributes->'labels') = 0
                OR (t.attributes->'labels' ?| sqlc.arg(labels)::text[])
            )
    ),
    -- Any active lock on a serial key blocks the chain.
    locked_serial_keys AS (
        SELECT DISTINCT t.serial_key
        FROM anclax.tasks t
        WHERE
            t.serial_key IS NOT NULL
            AND t.locked_at IS NOT NULL
            AND t.locked_at >= sqlc.arg(lock_expiry)
    ),
    -- A serial task is claimable only if there is no earlier pending task in its chain.
    candidate AS (
        SELECT e.id
        FROM eligible e
        WHERE
            e.serial_key IS NULL
            OR (
                NOT EXISTS (
                    SELECT 1 FROM locked_serial_keys l WHERE l.serial_key = e.serial_key
                )
                AND NOT EXISTS (
                    SELECT 1
                    FROM anclax.tasks s
                    WHERE
                        s.serial_key = e.serial_key
                        AND s.status = 'pending'
                        AND ROW(
                            s.serial_id IS NULL,
                            COALESCE(s.serial_id, 2147483647),
                            s.created_at,
                            COALESCE(s.started_at, '-infinity'::timestamptz),
                            s.id
                        ) < ROW(
                            e.serial_id IS NULL,
                            COALESCE(e.serial_id, 2147483647),
                            e.created_at,
                            COALESCE(e.started_at, '-infinity'::timestamptz),
                            e.id
                        )
                )
            )
        ORDER BY e.created_at, e.id
        LIMIT 1
    )
UPDATE anclax.tasks
SET
    locked_at = CURRENT_TIMESTAMP,
    worker_id = sqlc.arg(worker_id),
    attempts = attempts + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE
    anclax.tasks.id = (SELECT id FROM candidate)
    AND anclax.tasks.status = 'pending'
    AND (anclax.tasks.locked_at IS NULL OR anclax.tasks.locked_at < sqlc.arg(lock_expiry))
RETURNING *;

-- name: ClaimTaskByID :one
WITH
    eligible AS (
        SELECT t.*
        FROM anclax.tasks t
        WHERE
            t.id = sqlc.arg(id)
            AND t.status = 'pending'
            AND (t.started_at IS NULL OR t.started_at < NOW())
            AND (t.locked_at IS NULL OR t.locked_at < sqlc.arg(lock_expiry))
            AND (
                sqlc.arg(has_labels)::bool = false
                OR t.attributes->'labels' IS NULL
                OR jsonb_array_length(t.attributes->'labels') = 0
                OR (t.attributes->'labels' ?| sqlc.arg(labels)::text[])
            )
    ),
    locked_serial_keys AS (
        SELECT DISTINCT t.serial_key
        FROM anclax.tasks t
        WHERE
            t.serial_key IS NOT NULL
            AND t.locked_at IS NOT NULL
            AND t.locked_at >= sqlc.arg(lock_expiry)
    ),
    candidate AS (
        SELECT e.id
        FROM eligible e
        WHERE
            e.serial_key IS NULL
            OR (
                NOT EXISTS (
                    SELECT 1 FROM locked_serial_keys l WHERE l.serial_key = e.serial_key
                )
                AND NOT EXISTS (
                    SELECT 1
                    FROM anclax.tasks s
                    WHERE
                        s.serial_key = e.serial_key
                        AND s.status = 'pending'
                        AND ROW(
                            s.serial_id IS NULL,
                            COALESCE(s.serial_id, 2147483647),
                            s.created_at,
                            COALESCE(s.started_at, '-infinity'::timestamptz),
                            s.id
                        ) < ROW(
                            e.serial_id IS NULL,
                            COALESCE(e.serial_id, 2147483647),
                            e.created_at,
                            COALESCE(e.started_at, '-infinity'::timestamptz),
                            e.id
                        )
                )
            )
        LIMIT 1
    )
UPDATE anclax.tasks
SET
    locked_at = CURRENT_TIMESTAMP,
    worker_id = sqlc.arg(worker_id),
    attempts = attempts + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE
    anclax.tasks.id = (SELECT id FROM candidate)
    AND anclax.tasks.status = 'pending'
    AND (anclax.tasks.locked_at IS NULL OR anclax.tasks.locked_at < sqlc.arg(lock_expiry))
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
SET
    attributes = $2,
    spec = $3,
    started_at = $4,
    serial_key = $5,
    serial_id = $6,
    updated_at = CURRENT_TIMESTAMP
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
INSERT INTO anclax.tasks (attributes, spec, status, started_at, unique_tag, serial_key, serial_id)
VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (unique_tag) DO NOTHING RETURNING *;

-- name: GetTaskByUniqueTag :one
SELECT * FROM anclax.tasks
WHERE unique_tag = $1;

-- name: InsertEvent :one
INSERT INTO anclax.events (spec)
VALUES ($1)
RETURNING *;

-- name: GetLastTaskErrorEvent :one
SELECT * FROM anclax.events
WHERE spec->>'type' = 'TaskError'
  AND (spec->'taskError'->>'taskID')::int = sqlc.arg(task_id)::int
ORDER BY created_at DESC
LIMIT 1;

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
