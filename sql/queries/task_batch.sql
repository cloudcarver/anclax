-- name: ClaimTaskBatch :many
WITH unavailable_tags AS MATERIALIZED (
    SELECT c.tag FROM anclax.task_tag_limits c
    WHERE c.max_concurrency IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM anclax.task_tag_slots s WHERE s.tag=c.tag AND s.task_id IS NULL AND NOT s.retired
    )
), strict_candidates AS MATERIALIZED (
    SELECT t.id, t.priority, t.weight, t.created_at, 0::int AS group_order
    FROM anclax.tasks t
    WHERE t.status = 'pending'
        AND NOT EXISTS (SELECT 1 FROM anclax.task_tags tt JOIN unavailable_tags u ON u.tag=tt.tag WHERE tt.task_id=t.id)
        AND t.spec->>'type' NOT IN ('broadcastUpdateWorkerRuntimeConfig', 'applyWorkerRuntimeConfigToWorker', 'broadcastCancelTask', 'cancelTaskOnWorker', 'broadcastPauseTask', 'pauseTaskOnWorker')
        AND t.priority > 0
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
                WHERE head.serial_key = t.serial_key AND head.status = 'pending'
                    AND ROW(head.serial_id IS NULL, COALESCE(head.serial_id, 2147483647), head.created_at, COALESCE(head.started_at, '-infinity'::timestamptz), head.id)
                      < ROW(t.serial_id IS NULL, COALESCE(t.serial_id, 2147483647), t.created_at, COALESCE(t.started_at, '-infinity'::timestamptz), t.id)
            )
        ))
    ORDER BY t.priority DESC, t.created_at, t.id
    LIMIT sqlc.arg(strict_slots)::int
    FOR UPDATE OF t SKIP LOCKED
), normal_candidates AS MATERIALIZED (
    SELECT t.id, t.priority, t.weight, t.created_at, array_position(sqlc.arg(group_names)::text[], COALESCE((SELECT MIN(label) FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS labels(label) WHERE label = ANY(sqlc.arg(weighted_labels)::text[])), '__default__')) AS group_order
    FROM anclax.tasks t
    WHERE t.status = 'pending'
        AND NOT EXISTS (SELECT 1 FROM anclax.task_tags tt JOIN unavailable_tags u ON u.tag=tt.tag WHERE tt.task_id=t.id)
        AND t.spec->>'type' NOT IN ('broadcastUpdateWorkerRuntimeConfig', 'applyWorkerRuntimeConfigToWorker', 'broadcastCancelTask', 'cancelTaskOnWorker', 'broadcastPauseTask', 'pauseTaskOnWorker')
        AND t.priority = 0
        AND COALESCE((SELECT MIN(label) FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS labels(label) WHERE label = ANY(sqlc.arg(weighted_labels)::text[])), '__default__') = ANY(sqlc.arg(group_names)::text[])
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
                WHERE head.serial_key = t.serial_key AND head.status = 'pending'
                    AND ROW(head.serial_id IS NULL, COALESCE(head.serial_id, 2147483647), head.created_at, COALESCE(head.started_at, '-infinity'::timestamptz), head.id)
                      < ROW(t.serial_id IS NULL, COALESCE(t.serial_id, 2147483647), t.created_at, COALESCE(t.started_at, '-infinity'::timestamptz), t.id)
            )
        ))
    ORDER BY group_order, t.weight DESC, t.created_at, t.id
    LIMIT (sqlc.arg(batch_size)::int + 32)
    FOR UPDATE OF t SKIP LOCKED
), candidate AS MATERIALIZED (
    SELECT * FROM (SELECT * FROM strict_candidates UNION ALL SELECT * FROM normal_candidates) candidates
    ORDER BY priority DESC, group_order, CASE WHEN priority = 0 THEN weight ELSE 0 END DESC, created_at, id
), admitted AS MATERIALIZED (
    SELECT id FROM candidate
    WHERE anclax.try_admit_task_tags(candidate.id)
    LIMIT sqlc.arg(batch_size)::int
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
