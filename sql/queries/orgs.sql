-- name: CreateOrg :one
INSERT INTO anchor.orgs (name) VALUES ($1) RETURNING *;

-- name: GetOrg :one
SELECT * FROM anchor.orgs WHERE id = $1;

-- name: GetOrgByName :one
SELECT * FROM anchor.orgs WHERE name = $1;

-- name: InsertOrgOwner :one
INSERT INTO anchor.org_owners (org_id, user_id) VALUES ($1, $2) RETURNING *;

-- name: InsertOrgUser :one
INSERT INTO anchor.org_users (org_id, user_id) VALUES ($1, $2) RETURNING *;
