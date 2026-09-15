-- name: ConfigureWorkerPrefetch :exec
UPDATE anclax.workers SET prefetch_capacity=sqlc.arg(capacity)::int,
    prefetch_strict_percentage=COALESCE((SELECT (payload->>'maxStrictPercentage')::int
        FROM anclax.worker_runtime_configs ORDER BY version DESC LIMIT 1),sqlc.arg(strict_percentage)::int),
    prefetch_heartbeat_ttl_ms=GREATEST(9000,sqlc.arg(heartbeat_ttl_ms)::bigint)
    WHERE id=sqlc.arg(worker_id);

-- name: EnsureTaskPrefetch :exec
INSERT INTO anclax.tasks(attributes,spec,status,unique_tag)
VALUES ('{"retryPolicy":{"interval":"100ms","maxAttempts":-1}}',
    '{"type":"prefetchTasks","payload":{}}','pending','anclax:system:prefetch')
ON CONFLICT(unique_tag) DO NOTHING;

-- name: ListWorkerPrefetchConsumption :many
-- Include offline counters so liveness changes don't replay historical claims.
SELECT id,prefetch_claimed,CASE WHEN status='online'
    AND last_heartbeat>statement_timestamp()-prefetch_heartbeat_ttl_ms*interval '1 millisecond'
    THEN prefetch_capacity ELSE 0 END::int AS capacity
FROM anclax.workers WHERE prefetch_capacity>0;

-- name: PrefetchReadyTasks :one
-- A negative prepared count means the scheduler attempt no longer owns its lease.
SELECT anclax.prefetch_ready_tasks(sqlc.arg(task_id)::int,sqlc.arg(worker_id)::uuid,
    sqlc.arg(lease_version)::bigint,sqlc.arg(batch_size)::int,
    sqlc.arg(ready_ttl_ms)::bigint,sqlc.arg(lock_ttl_ms)::bigint)::int AS prepared;
