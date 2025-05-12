-- name: CreateKeyPair :one
INSERT INTO anchor.access_key_pairs (access_key, secret_key)
VALUES ($1, $2)
RETURNING *;

-- name: GetKeyPair :one
SELECT * FROM anchor.access_key_pairs
WHERE access_key = $1;

-- name: DeleteKeyPair :exec
DELETE FROM anchor.access_key_pairs
WHERE access_key = $1;
