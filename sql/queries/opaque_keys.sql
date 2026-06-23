-- name: CreateOpaqueKey :one
INSERT INTO anclax.opaque_keys ("group", key) VALUES ($1, $2) RETURNING id;

-- name: GetOpaqueKey :one
SELECT key FROM anclax.opaque_keys WHERE id = $1;

-- name: DeleteOpaqueKey :exec
DELETE FROM anclax.opaque_keys WHERE id = $1;

-- name: DeleteOpaqueKeys :exec
DELETE FROM anclax.opaque_keys WHERE "group" = $1;
