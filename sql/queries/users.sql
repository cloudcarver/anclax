-- name: CreateUser :one
INSERT INTO anclax.users (
    name,
    password_hash,
    password_salt
) VALUES (
    $1, $2, $3
) RETURNING *;

-- name: GetUser :one
SELECT * FROM anclax.users
WHERE id = $1 AND deleted_at IS NULL;

-- name: GetUserByName :one
SELECT * FROM anclax.users WHERE name = $1 AND deleted_at IS NULL;

-- name: IsUsernameExists :one
SELECT EXISTS (SELECT 1 FROM anclax.users WHERE name = $1);

-- name: DeleteUserByName :exec
UPDATE anclax.users SET deleted_at = CURRENT_TIMESTAMP WHERE name = $1;

-- name: RestoreUserByName :exec
UPDATE anclax.users SET deleted_at = NULL WHERE name = $1;

-- name: SetUserDefaultOrg :exec
INSERT INTO anclax.user_default_orgs (user_id, org_id)
VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE SET org_id = $2;

-- name: GetUserDefaultOrg :one
SELECT org_id FROM anclax.user_default_orgs
WHERE user_id = $1;

-- name: UpdateUserPassword :exec
UPDATE anclax.users SET password_hash = $2, password_salt = $3 WHERE id = $1;
