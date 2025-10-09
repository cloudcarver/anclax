-- name: CreateOrg :one
INSERT INTO anclax.orgs (name) VALUES ($1) RETURNING *;

-- name: GetOrg :one
SELECT * FROM anclax.orgs WHERE id = $1;

-- name: GetOrgByName :one
SELECT * FROM anclax.orgs WHERE name = $1;

-- name: InsertOrgOwner :one
INSERT INTO anclax.org_owners (org_id, user_id) VALUES ($1, $2) RETURNING *;

-- name: InsertOrgUser :one
INSERT INTO anclax.org_users (org_id, user_id) VALUES ($1, $2) RETURNING *;

-- name: ListOrgs :many
SELECT orgs.*
FROM anclax.org_users 
JOIN anclax.orgs AS orgs ON anclax.org_users.org_id = orgs.id
WHERE anclax.org_users.user_id = $1;

-- name: GetUserDefaultOrg :one
