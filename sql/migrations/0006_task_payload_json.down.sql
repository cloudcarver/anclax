BEGIN;

UPDATE anclax.tasks
SET spec = jsonb_set(
    spec,
    '{payload}',
    encode(convert_to(spec->>'payload', 'UTF8'), 'base64')::jsonb
)
WHERE spec ? 'payload'
  AND jsonb_typeof(spec->'payload') = 'object';

COMMIT;
