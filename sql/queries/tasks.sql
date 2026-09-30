-- name: ClaimTask :one
WITH unavailable_tags AS MATERIALIZED (
    SELECT c.tag FROM anclax.task_tag_limits c
    WHERE c.max_concurrency IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM anclax.task_tag_slots s WHERE s.tag=c.tag AND s.task_id IS NULL AND NOT s.retired
    )
), candidate AS MATERIALIZED (
    SELECT t.id
    FROM anclax.tasks t
    WHERE t.status = 'pending'
        AND (anclax.is_system_task(t.spec->>'type')
            OR NOT EXISTS (SELECT 1 FROM anclax.task_tags tt JOIN unavailable_tags u ON u.tag=tt.tag WHERE tt.task_id=t.id))
        AND (t.started_at IS NULL OR t.started_at <= statement_timestamp())
        AND (t.locked_at IS NULL OR COALESCE(t.lease_expires_at, t.locked_at + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond') <= statement_timestamp())
        AND NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS task_label(value)
            WHERE NOT (task_label.value = ANY(COALESCE(sqlc.arg(labels)::text[], ARRAY[]::text[])))
        )
        AND (t.serial_key IS NULL OR (
            NOT EXISTS (
                SELECT 1 FROM anclax.tasks active
                WHERE active.serial_key = t.serial_key
                    AND (active.lease_expires_at IS NOT NULL OR active.locked_at IS NOT NULL)
                    AND COALESCE(active.lease_expires_at, active.locked_at + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond') > statement_timestamp()
            )
            AND NOT EXISTS (
                SELECT 1 FROM anclax.tasks head
                WHERE head.serial_key = t.serial_key AND head.status IN ('pending','ready')
                    AND ROW(head.serial_id IS NULL, COALESCE(head.serial_id, 2147483647), head.created_at, COALESCE(head.started_at, '-infinity'::timestamptz), head.id)
                      < ROW(t.serial_id IS NULL, COALESCE(t.serial_id, 2147483647), t.created_at, COALESCE(t.started_at, '-infinity'::timestamptz), t.id)
            )
        ))
    ORDER BY t.priority DESC, t.created_at, t.id
    LIMIT 32
    FOR UPDATE OF t SKIP LOCKED
), admitted AS MATERIALIZED (
    SELECT id FROM candidate
    WHERE anclax.try_admit_task_tags(candidate.id,sqlc.arg(lock_ttl_ms)::bigint)
    LIMIT 1
)
UPDATE anclax.tasks AS t
SET locked_at = statement_timestamp(), worker_id = sqlc.arg(worker_id),
    lease_expires_at = statement_timestamp() + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond',
    lease_duration_ms = sqlc.arg(lock_ttl_ms)::bigint,
    lease_version = t.lease_version + 1, attempts = t.attempts + 1,
    updated_at = statement_timestamp()
FROM admitted
WHERE t.id = admitted.id
RETURNING t.*;

-- name: ClaimStrictTask :one
WITH unavailable_tags AS MATERIALIZED (
    SELECT c.tag FROM anclax.task_tag_limits c
    WHERE c.max_concurrency IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM anclax.task_tag_slots s WHERE s.tag=c.tag AND s.task_id IS NULL AND NOT s.retired
    )
), candidate AS MATERIALIZED (
    SELECT t.id
    FROM anclax.tasks t
    WHERE t.status = 'pending'
        AND NOT EXISTS (SELECT 1 FROM anclax.task_tags tt JOIN unavailable_tags u ON u.tag=tt.tag WHERE tt.task_id=t.id)
        AND t.priority > 0
        AND NOT anclax.is_system_task(t.spec->>'type')
        AND (t.started_at IS NULL OR t.started_at <= statement_timestamp())
        AND (t.locked_at IS NULL OR COALESCE(t.lease_expires_at, t.locked_at + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond') <= statement_timestamp())
        AND NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS task_label(value)
            WHERE NOT (task_label.value = ANY(COALESCE(sqlc.arg(labels)::text[], ARRAY[]::text[])))
        )
        AND (t.serial_key IS NULL OR (
            NOT EXISTS (
                SELECT 1 FROM anclax.tasks active
                WHERE active.serial_key = t.serial_key
                    AND (active.lease_expires_at IS NOT NULL OR active.locked_at IS NOT NULL)
                    AND COALESCE(active.lease_expires_at, active.locked_at + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond') > statement_timestamp()
            )
            AND NOT EXISTS (
                SELECT 1 FROM anclax.tasks head
                WHERE head.serial_key = t.serial_key AND head.status IN ('pending','ready')
                    AND ROW(head.serial_id IS NULL, COALESCE(head.serial_id, 2147483647), head.created_at, COALESCE(head.started_at, '-infinity'::timestamptz), head.id)
                      < ROW(t.serial_id IS NULL, COALESCE(t.serial_id, 2147483647), t.created_at, COALESCE(t.started_at, '-infinity'::timestamptz), t.id)
            )
        ))
    ORDER BY t.priority DESC, t.created_at, t.id
    LIMIT 32
    FOR UPDATE OF t SKIP LOCKED
), admitted AS MATERIALIZED (
    SELECT id FROM candidate
    WHERE anclax.try_admit_task_tags(candidate.id,sqlc.arg(lock_ttl_ms)::bigint)
    LIMIT 1
)
UPDATE anclax.tasks AS t
SET locked_at = statement_timestamp(), worker_id = sqlc.arg(worker_id),
    lease_expires_at = statement_timestamp() + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond',
    lease_duration_ms = sqlc.arg(lock_ttl_ms)::bigint,
    lease_version = t.lease_version + 1, attempts = t.attempts + 1,
    updated_at = statement_timestamp()
FROM admitted
WHERE t.id = admitted.id
RETURNING t.*;

-- name: ClaimNormalTaskByGroup :one
WITH unavailable_tags AS MATERIALIZED (
    SELECT c.tag FROM anclax.task_tag_limits c
    WHERE c.max_concurrency IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM anclax.task_tag_slots s WHERE s.tag=c.tag AND s.task_id IS NULL AND NOT s.retired
    )
), candidate AS MATERIALIZED (
    SELECT t.id
    FROM anclax.tasks t
    WHERE t.status = 'pending'
        AND NOT EXISTS (SELECT 1 FROM anclax.task_tags tt JOIN unavailable_tags u ON u.tag=tt.tag WHERE tt.task_id=t.id)
        AND NOT anclax.is_system_task(t.spec->>'type')
        AND (
            (sqlc.arg(allow_strict)::boolean AND t.priority > 0)
            OR (t.priority = 0 AND COALESCE((
                SELECT MIN(label) FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS labels(label)
                WHERE label = ANY(sqlc.arg(weighted_labels)::text[])
            ), '__default__') = sqlc.arg(group_name)::text)
        )
        AND (t.started_at IS NULL OR t.started_at <= statement_timestamp())
        AND (t.locked_at IS NULL OR COALESCE(t.lease_expires_at, t.locked_at + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond') <= statement_timestamp())
        AND NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS task_label(value)
            WHERE NOT (task_label.value = ANY(COALESCE(sqlc.arg(labels)::text[], ARRAY[]::text[])))
        )
        AND (t.serial_key IS NULL OR (
            NOT EXISTS (
                SELECT 1 FROM anclax.tasks active
                WHERE active.serial_key = t.serial_key
                    AND (active.lease_expires_at IS NOT NULL OR active.locked_at IS NOT NULL)
                    AND COALESCE(active.lease_expires_at, active.locked_at + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond') > statement_timestamp()
            )
            AND NOT EXISTS (
                SELECT 1 FROM anclax.tasks head
                WHERE head.serial_key = t.serial_key AND head.status IN ('pending','ready')
                    AND ROW(head.serial_id IS NULL, COALESCE(head.serial_id, 2147483647), head.created_at, COALESCE(head.started_at, '-infinity'::timestamptz), head.id)
                      < ROW(t.serial_id IS NULL, COALESCE(t.serial_id, 2147483647), t.created_at, COALESCE(t.started_at, '-infinity'::timestamptz), t.id)
            )
        ))
    ORDER BY t.priority DESC, CASE WHEN t.priority = 0 THEN t.weight END DESC, t.created_at, t.id
    LIMIT 32
    FOR UPDATE OF t SKIP LOCKED
), admitted AS MATERIALIZED (
    SELECT id FROM candidate
    WHERE anclax.try_admit_task_tags(candidate.id,sqlc.arg(lock_ttl_ms)::bigint)
    LIMIT 1
)
UPDATE anclax.tasks AS t
SET locked_at = statement_timestamp(), worker_id = sqlc.arg(worker_id),
    lease_expires_at = statement_timestamp() + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond',
    lease_duration_ms = sqlc.arg(lock_ttl_ms)::bigint,
    lease_version = t.lease_version + 1, attempts = t.attempts + 1,
    updated_at = statement_timestamp()
FROM admitted
WHERE t.id = admitted.id
RETURNING t.*;

-- name: ClaimTaskByID :one
WITH candidate AS MATERIALIZED (
    SELECT t.id
    FROM anclax.tasks t
    WHERE (t.status='pending' OR t.status='ready')
        AND t.id = sqlc.arg(id)
        AND (t.priority = 0 OR sqlc.arg(allow_strict)::boolean)
        AND (t.started_at IS NULL OR t.started_at <= statement_timestamp())
        AND (t.locked_at IS NULL OR COALESCE(t.lease_expires_at, t.locked_at + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond') <= statement_timestamp())
        AND NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS task_label(value)
            WHERE NOT (task_label.value = ANY(COALESCE(sqlc.arg(labels)::text[], ARRAY[]::text[])))
        )
        AND (t.status='ready' OR t.serial_key IS NULL OR (
            NOT EXISTS (
                SELECT 1 FROM anclax.tasks active
                WHERE active.serial_key = t.serial_key
                    AND (active.lease_expires_at IS NOT NULL OR active.locked_at IS NOT NULL)
                    AND COALESCE(active.lease_expires_at, active.locked_at + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond') > statement_timestamp()
            )
            AND NOT EXISTS (
                SELECT 1 FROM anclax.tasks head
                WHERE head.serial_key = t.serial_key AND head.status IN ('pending','ready')
                    AND ROW(head.serial_id IS NULL, COALESCE(head.serial_id, 2147483647), head.created_at, COALESCE(head.started_at, '-infinity'::timestamptz), head.id)
                      < ROW(t.serial_id IS NULL, COALESCE(t.serial_id, 2147483647), t.created_at, COALESCE(t.started_at, '-infinity'::timestamptz), t.id)
            )
        ))
    ORDER BY t.id
    LIMIT 1
    FOR UPDATE OF t SKIP LOCKED
), admitted AS MATERIALIZED (
    SELECT id FROM candidate
    WHERE anclax.try_admit_task_tags(candidate.id,sqlc.arg(lock_ttl_ms)::bigint)
    LIMIT 1
)
UPDATE anclax.tasks AS t
SET status=CASE WHEN t.status='ready' THEN 'running' ELSE t.status END, locked_at = statement_timestamp(), worker_id = sqlc.arg(worker_id),
    lease_expires_at = statement_timestamp() + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond',
    lease_duration_ms = sqlc.arg(lock_ttl_ms)::bigint,
    lease_version = CASE WHEN t.status='ready' THEN t.lease_version ELSE t.lease_version+1 END, attempts = t.attempts + 1,
    updated_at = statement_timestamp()
FROM admitted
WHERE t.id = admitted.id
RETURNING t.*;

-- name: ClaimWorkerCommand :one
WITH candidate AS MATERIALIZED (
    SELECT t.id
    FROM anclax.tasks t
    WHERE t.status IN ('pending','running')
        AND anclax.is_system_task(t.spec->>'type')
        AND (t.started_at IS NULL OR t.started_at <= statement_timestamp())
        AND (t.locked_at IS NULL OR COALESCE(t.lease_expires_at, t.locked_at + sqlc.arg(lock_ttl_ms)::bigint * INTERVAL '1 millisecond') <= statement_timestamp())
        AND NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS task_label(value)
            WHERE NOT (task_label.value = ANY(COALESCE(sqlc.arg(labels)::text[], ARRAY[]::text[])))
        )
    ORDER BY COALESCE(t.started_at,t.created_at),t.id LIMIT 1 FOR UPDATE OF t SKIP LOCKED
)
UPDATE anclax.tasks AS t
SET status='running',locked_at=statement_timestamp(),worker_id=sqlc.arg(worker_id),
    lease_expires_at=statement_timestamp()+sqlc.arg(lock_ttl_ms)::bigint*INTERVAL '1 millisecond',
    lease_duration_ms=sqlc.arg(lock_ttl_ms)::bigint,
    lease_version=t.lease_version+1,attempts=t.attempts+1,updated_at=statement_timestamp()
FROM candidate WHERE t.id=candidate.id RETURNING t.*;

-- name: ListAllPendingTasks :many
SELECT * FROM anclax.tasks
WHERE
    status IN ('pending','ready','running')
    AND (
        started_at IS NULL OR started_at < NOW()
    );

-- name: UpdateTaskStatus :exec
UPDATE anclax.tasks
SET
    status = $2,
    lease_version = CASE WHEN $2 = 'pending' THEN lease_version + 1 ELSE lease_version END,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND (
    ($2 = 'paused' AND status IN ('pending', 'ready', 'running', 'paused'))
    OR ($2 = 'cancelled' AND status IN ('pending', 'ready', 'running', 'paused', 'cancelled'))
    OR ($2 = 'pending' AND status = 'paused')
    OR ($2 IN ('completed', 'failed') AND status IN ('pending', 'ready', 'running'))
);

-- name: UpdateTaskStatusByWorker :one
UPDATE anclax.tasks
SET
    status = $2,
    locked_at = NULL,
    worker_id = NULL,
    lease_expires_at = NULL, lease_duration_ms = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND worker_id = $3 AND lease_version = sqlc.arg(lease_version) AND status IN ('pending', 'running')
RETURNING id;

-- name: UpdateTask :exec
UPDATE anclax.tasks
SET
    attributes = $2,
    spec = $3,
    started_at = $4,
    serial_key = $5,
    serial_id = $6,
    priority = $7,
    weight = $8,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateTaskStartedAt :exec
UPDATE anclax.tasks
SET started_at = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateTaskStartedAtByWorker :one
UPDATE anclax.tasks
SET started_at = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND worker_id = $3 AND lease_version = sqlc.arg(lease_version) AND status IN ('pending', 'running')
RETURNING id;

-- name: RefreshTaskLock :one
UPDATE anclax.tasks
SET locked_at = statement_timestamp(), updated_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + lease_duration_ms * INTERVAL '1 millisecond'
WHERE id = $1 AND worker_id = $2 AND lease_version = sqlc.arg(lease_version) AND status IN ('pending', 'running')
    AND lease_expires_at > statement_timestamp()
RETURNING id;

-- name: ReleaseTaskLockByWorker :one
UPDATE anclax.tasks
SET locked_at = NULL, worker_id = NULL, lease_expires_at = NULL, lease_duration_ms = NULL, updated_at = statement_timestamp()
WHERE id = $1 AND worker_id = $2 AND lease_version = sqlc.arg(lease_version)
RETURNING id;

-- name: RefreshTaskLocks :many
WITH attempts AS (
    SELECT unnest(sqlc.arg(ids)::int[]) AS id,
           unnest(sqlc.arg(lease_versions)::bigint[]) AS lease_version
)
UPDATE anclax.tasks AS t
SET locked_at = statement_timestamp(), updated_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + t.lease_duration_ms * INTERVAL '1 millisecond'
FROM attempts AS a
WHERE t.id = a.id AND t.lease_version = a.lease_version
    AND t.worker_id = sqlc.arg(worker_id)
    AND t.status IN ('pending', 'running')
    AND t.lease_expires_at > statement_timestamp()
RETURNING t.id, t.lease_version;

-- name: CreateTask :one
INSERT INTO anclax.tasks (attributes, spec, status, started_at, unique_tag, parent_task_id, serial_key, serial_id, priority, weight)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (unique_tag) DO NOTHING RETURNING *;

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

-- name: GetTaskWaitStatusByID :one
SELECT id, status
FROM anclax.tasks
WHERE id = $1;

-- name: ListTerminalTaskWaitStatuses :many
SELECT id, status
FROM anclax.tasks
WHERE id = ANY(sqlc.arg(ids)::int[])
  AND status IN ('completed', 'failed', 'cancelled');

-- name: ListTaskWaitStatuses :many
SELECT id, status
FROM anclax.tasks
WHERE id = ANY(sqlc.arg(ids)::int[]);

-- name: ListTaskDescendantIDs :many
WITH RECURSIVE descendants AS (
    SELECT t.id
    FROM anclax.tasks t
    WHERE t.parent_task_id = $1
    UNION ALL
    SELECT t.id
    FROM anclax.tasks t
    JOIN descendants d ON t.parent_task_id = d.id
)
SELECT id FROM descendants;

-- name: ListTaskIDsByTags :many
SELECT t.id
FROM anclax.tasks t
WHERE
    COALESCE(array_length(sqlc.arg(tags)::text[], 1), 0) > 0
    AND t.attributes->'tags' IS NOT NULL
    AND jsonb_array_length(t.attributes->'tags') > 0
    AND NOT EXISTS (
        SELECT 1
        FROM unnest(sqlc.arg(tags)::text[]) AS required_tag(value)
        WHERE NOT (t.attributes->'tags' ? required_tag.value)
    )
    AND NOT EXISTS (
        SELECT 1
        FROM jsonb_array_elements(sqlc.arg(except_tag_sets)::jsonb) AS except_set(tags)
        WHERE jsonb_typeof(except_set.tags) = 'array'
            AND jsonb_array_length(except_set.tags) > 0
            AND NOT EXISTS (
                SELECT 1
                FROM jsonb_array_elements_text(except_set.tags) AS except_tag(value)
                WHERE NOT (t.attributes->'tags' ? except_tag.value)
            )
    )
ORDER BY t.id;

-- name: IncrementAttempts :exec
UPDATE anclax.tasks
SET attempts = attempts + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: VerifyTaskOwnership :one
SELECT id FROM anclax.tasks
WHERE id = $1 AND worker_id = $2 AND lease_version = sqlc.arg(lease_version);

-- name: UpdatePendingTaskPriorityByLabels :execrows
UPDATE anclax.tasks
SET
    priority = GREATEST(sqlc.arg(priority)::int, 0),
    attributes = jsonb_set(attributes, '{priority}', to_jsonb(GREATEST(sqlc.arg(priority)::int, 0)), true),
    updated_at = CURRENT_TIMESTAMP
WHERE
    status = 'pending'
    AND (
        (sqlc.arg(has_labels)::bool = true AND attributes->'labels' ?| sqlc.arg(labels)::text[])
        OR (
            sqlc.arg(has_labels)::bool = false
            AND (
                attributes->'labels' IS NULL
                OR jsonb_array_length(attributes->'labels') = 0
            )
        )
    );

-- name: UpdatePendingTaskWeightByLabels :execrows
UPDATE anclax.tasks
SET
    weight = GREATEST(sqlc.arg(weight)::int, 1),
    attributes = jsonb_set(attributes, '{weight}', to_jsonb(GREATEST(sqlc.arg(weight)::int, 1)), true),
    updated_at = CURRENT_TIMESTAMP
WHERE
    status = 'pending'
    AND (
        (sqlc.arg(has_labels)::bool = true AND attributes->'labels' ?| sqlc.arg(labels)::text[])
        OR (
            sqlc.arg(has_labels)::bool = false
            AND (
                attributes->'labels' IS NULL
                OR jsonb_array_length(attributes->'labels') = 0
            )
        )
    );

-- name: FinalizeTaskAttempt :one
UPDATE anclax.tasks
SET
    status = CASE WHEN status IN ('paused', 'cancelled') THEN status ELSE sqlc.arg(status)::text END,
    started_at = CASE WHEN status IN ('pending', 'running') THEN sqlc.narg(started_at)::timestamptz ELSE started_at END,
    attempts = CASE WHEN status IN ('pending', 'running') THEN sqlc.arg(attempts)::int ELSE attempts END,
    locked_at = NULL,
    worker_id = NULL,
    lease_expires_at = NULL, lease_duration_ms = NULL,
    updated_at = statement_timestamp()
WHERE id = sqlc.arg(id) AND worker_id = sqlc.arg(worker_id)
    AND lease_version = sqlc.arg(lease_version)
    AND status IN ('pending', 'ready', 'running', 'paused', 'cancelled')
RETURNING status;

-- name: GetTaskAttemptStatus :one
SELECT status FROM anclax.tasks
WHERE id = sqlc.arg(id) AND worker_id = sqlc.arg(worker_id) AND lease_version = sqlc.arg(lease_version);
