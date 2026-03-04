-- name: CreateStateMachine :one
INSERT INTO anclax.fsm (type, state) VALUES ($1, $2) RETURNING id;

-- name: GetStateMachineByID :one
SELECT id, type, state, created_at, updated_at FROM anclax.fsm WHERE id = $1;

-- name: UpdateStateMachineStateCAS :exec
UPDATE anclax.fsm SET state = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND state = $3;
