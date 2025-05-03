-- name: CreateOrg :one
INSERT INTO orgs (name) VALUES ($1) RETURNING *;

-- name: GetOrg :one
SELECT * FROM orgs WHERE id = $1;

-- name: GetOrgByName :one
SELECT * FROM orgs WHERE name = $1;

-- name: InsertOrgOwner :one
INSERT INTO org_owners (org_id, user_id) VALUES ($1, $2) RETURNING *;

-- name: InsertOrgUser :one
INSERT INTO org_users (org_id, user_id) VALUES ($1, $2) RETURNING *;
