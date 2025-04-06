-- name: GetKeys :many
SELECT * FROM keys WHERE expired_at > NOW();

-- name: StoreKey :one
INSERT INTO keys (public_key, private_key, expired_at) VALUES ($1, $2, $3) RETURNING *;

-- name: CleanUpKeys :exec
DELETE FROM keys WHERE expired_at < NOW();

-- name: GetKeyByID :one
SELECT * FROM keys WHERE id = $1 AND expired_at > NOW();

-- name: GetLatestKey :one
SELECT * FROM keys ORDER BY created_at DESC LIMIT 1;
