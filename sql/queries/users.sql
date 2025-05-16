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

-- name: SetUserDefaultOrg :exec
INSERT INTO anchor.user_default_orgs (user_id, org_id)
VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE SET org_id = $2;

-- name: GetUserDefaultOrg :one
SELECT org_id FROM anchor.user_default_orgs
WHERE user_id = $1;
