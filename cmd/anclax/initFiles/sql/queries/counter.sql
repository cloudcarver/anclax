-- name: GetCounter :one
SELECT * FROM counter WHERE id = 1;

-- name: IncrementCounter :exec
UPDATE counter SET value = value + 1 WHERE id = 1;
