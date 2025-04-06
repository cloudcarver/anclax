-- name: StoreRefreshToken :exec
INSERT INTO refresh_tokens (token, expired_at)
VALUES ($1, $2);

-- name: DeleteRefreshToken :exec
DELETE FROM refresh_tokens
WHERE token = $1;

-- name: GetRefreshToken :one
SELECT * FROM refresh_tokens
WHERE token = $1;
