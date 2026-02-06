BEGIN;

UPDATE anclax.tasks
SET spec = jsonb_set(
    spec,
    '{payload}',
    convert_from(decode(spec->>'payload', 'base64'), 'UTF8')::jsonb
)
WHERE spec ? 'payload'
  AND jsonb_typeof(spec->'payload') = 'string'
  AND (spec->>'payload') ~ '^[A-Za-z0-9+/]+={0,2}$';

COMMIT;
