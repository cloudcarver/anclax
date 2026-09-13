-- name: SetTaskTagConcurrencyLimit :exec
INSERT INTO anclax.task_tag_concurrency (tag, max_concurrency)
VALUES (sqlc.arg(tag), sqlc.arg(max_concurrency)::int)
ON CONFLICT (tag) DO UPDATE SET max_concurrency = EXCLUDED.max_concurrency;

-- name: RemoveTaskTagConcurrencyLimit :exec
UPDATE anclax.task_tag_concurrency SET max_concurrency = NULL WHERE tag = sqlc.arg(tag);

-- name: GetTaskTagConcurrency :one
SELECT tag, max_concurrency, in_use FROM anclax.task_tag_concurrency WHERE tag = sqlc.arg(tag);

-- name: ListTaskTagConcurrencyLimits :many
SELECT tag, max_concurrency, in_use FROM anclax.task_tag_concurrency
WHERE max_concurrency IS NOT NULL AND tag > sqlc.arg(after_tag)::text
ORDER BY tag LIMIT sqlc.arg(page_size)::int;

-- name: MaintainTaskConcurrency :exec
SELECT anclax.maintain_task_concurrency(sqlc.arg(legacy_ttl_ms)::bigint);
