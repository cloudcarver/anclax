-- name: ClaimTaskBatch :many
WITH strict_candidates AS MATERIALIZED (
    SELECT t.id, t.priority, t.weight, t.created_at, 0::int AS group_order
    FROM anclax.tasks t
    WHERE t.status = 'ready' AND t.ready_expires_at > statement_timestamp()
        AND t.priority > 0
        AND NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS task_label(value)
            WHERE NOT (task_label.value = ANY(COALESCE(sqlc.arg(labels)::text[], ARRAY[]::text[])))
        )
    ORDER BY t.priority DESC, t.created_at, t.id
    LIMIT sqlc.arg(strict_slots)::int
    FOR UPDATE OF t SKIP LOCKED
), normal_candidates AS MATERIALIZED (
    SELECT t.id, t.priority, t.weight, t.created_at, array_position(sqlc.arg(group_names)::text[], COALESCE((SELECT MIN(label) FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS labels(label) WHERE label = ANY(sqlc.arg(weighted_labels)::text[])), '__default__')) AS group_order
    FROM anclax.tasks t
    WHERE t.status = 'ready' AND t.ready_expires_at > statement_timestamp()
        AND t.priority = 0
        AND COALESCE((SELECT MIN(label) FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS labels(label) WHERE label = ANY(sqlc.arg(weighted_labels)::text[])), '__default__') = ANY(sqlc.arg(group_names)::text[])
        AND NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'labels', 'null'::jsonb), '[]'::jsonb)) AS task_label(value)
            WHERE NOT (task_label.value = ANY(COALESCE(sqlc.arg(labels)::text[], ARRAY[]::text[])))
        )
    ORDER BY group_order, t.weight DESC, t.created_at, t.id
    LIMIT sqlc.arg(batch_size)::int
    FOR UPDATE OF t SKIP LOCKED
), candidate AS MATERIALIZED (
    SELECT * FROM (SELECT * FROM strict_candidates UNION ALL SELECT * FROM normal_candidates) candidates
    ORDER BY priority DESC,group_order,CASE WHEN priority=0 THEN weight ELSE 0 END DESC,created_at,id
    LIMIT sqlc.arg(batch_size)::int
)
UPDATE anclax.tasks AS t
SET status='running',ready_expires_at=NULL,locked_at=statement_timestamp(),worker_id=sqlc.arg(worker_id),
    lease_expires_at=statement_timestamp()+sqlc.arg(lock_ttl_ms)::bigint*INTERVAL '1 millisecond',
    lease_duration_ms=sqlc.arg(lock_ttl_ms)::bigint,attempts=t.attempts+1,updated_at=statement_timestamp()
FROM candidate WHERE t.id=candidate.id RETURNING t.*;
