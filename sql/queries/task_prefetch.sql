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

-- name: PrefetchReadyTasks :one
SELECT anclax.prefetch_ready_tasks(sqlc.arg(task_id)::int,sqlc.arg(worker_id)::uuid,
    sqlc.arg(lease_version)::bigint,sqlc.arg(batch_size)::int,
    sqlc.arg(ready_ttl_ms)::bigint,sqlc.arg(lock_ttl_ms)::bigint)::int AS prepared;
