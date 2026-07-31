-- name: CreateOpaqueKey :one
INSERT INTO anclax.opaque_keys ("group", key, expires_at)
VALUES (
    $1,
    $2,
    CURRENT_TIMESTAMP + sqlc.arg(ttl_microseconds)::BIGINT * INTERVAL '1 microsecond'
)
RETURNING id, expires_at;

-- name: GetOpaqueKey :one
SELECT key
FROM anclax.opaque_keys
WHERE id = $1
  AND expires_at > CURRENT_TIMESTAMP;

-- name: ConsumeOpaqueKey :one
DELETE FROM anclax.opaque_keys
WHERE id = sqlc.arg(id)
  AND key = sqlc.arg(key)
  AND expires_at > CURRENT_TIMESTAMP
RETURNING id;

-- name: DeleteOpaqueKey :exec
DELETE FROM anclax.opaque_keys WHERE id = $1;

-- name: DeleteOpaqueKeys :exec
DELETE FROM anclax.opaque_keys WHERE "group" = $1;
