-- name: CreateUser :one
INSERT INTO anchor.users (
    name,
    password_hash,
    password_salt
) VALUES (
    $1, $2, $3
) RETURNING *;

-- name: GetUser :one
SELECT * FROM anchor.users
WHERE id = $1;

-- name: GetUserByName :one
SELECT * FROM anchor.users
WHERE name = $1;

-- name: DeleteUserByName :exec
DELETE FROM anchor.users
WHERE name = $1;
