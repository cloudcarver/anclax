-- name: CreateOpaqueKey :one
INSERT INTO anchor.opaque_keys (user_id, key) VALUES ($1, $2) RETURNING id;

-- name: GetOpaqueKey :one
SELECT key FROM anchor.opaque_keys WHERE id = $1;

-- name: DeleteOpaqueKey :exec
DELETE FROM anchor.opaque_keys WHERE id = $1;

-- name: DeleteOpaqueKeys :exec
DELETE FROM anchor.opaque_keys WHERE user_id = $1;
