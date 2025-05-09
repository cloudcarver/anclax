-- name: CreateUser :one
INSERT INTO users (
    name,
    password_hash,
    password_salt
) VALUES (
    $1, $2, $3
) RETURNING *;

-- name: GetUser :one
SELECT * FROM users
WHERE id = $1;

-- name: GetUserByName :one
SELECT * FROM users
WHERE name = $1;

-- name: DeleteUserByName :exec
DELETE FROM users
WHERE name = $1;

