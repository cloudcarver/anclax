-- name: GetCounter :one
SELECT * FROM counter WHERE id = 1;

-- name: IncrementCounter :exec
UPDATE counter SET value = value + sqlc.arg(amount)::integer WHERE id = 1;
