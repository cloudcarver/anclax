# myexampleapp 

This project is initialized by `anclax init`.

Use skill:

```
npx skills add cloudcarver/anclax
```

## File structure

- `app/`: application bootstrap. The default template keeps one app here. In a larger repo, you can grow this into `app/service_a`, `app/service_b`, and so on. Each service can have its own `app.go`, `injection.go`, and `wire/`.
- `api/`: OpenAPI fragments under `api/openapi/`, task definitions under `api/tasks/`, and shared schemas under `api/schemas/`.
- `pkg/`: reusable modules shared by apps and services.
- `sql/`: shared queries and migrations for the default shared model.
- `pkg/zgen/`: generated code. Do not edit by hand.
- `.anclax/bin/`: pinned external tools used by `anclax gen`.

## Quick test

```bash
umask 077
test -f .env || printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 32)" > .env
docker compose up -d

printf 'Username: '
read -r ANCLAX_USERNAME
export ANCLAX_USERNAME
export ANCLAX_PASSWORD="$(openssl rand -hex 24)"
curl -X POST http://localhost:2910/api/v1/auth/sign-up -H "Content-Type: application/json" -d "{\"name\": \"${ANCLAX_USERNAME}\", \"password\": \"${ANCLAX_PASSWORD}\"}"

# Copy accessToken from the sign-up response before making authenticated requests.
export ANCLAX_ACCESS_TOKEN=your_access_token
curl http://localhost:2910/api/v1/counter
curl -X POST http://localhost:2910/api/v1/counter -H "Content-Type: application/json" -H "Authorization: Bearer ${ANCLAX_ACCESS_TOKEN}"
curl http://localhost:2910/api/v1/counter
```

The generated `.env` file is ignored by Git. Keep it private; create a new value instead of committing it.

## Multi-service pattern

Anclax works well for service-oriented repos in a single codebase:

- Keep reusable modules in `pkg/`.
- By default, all apps can share `pkg/model` and the top-level `sql/` folder.
- If one service needs its own persistence layer, give it its own `sql/` folder and model package under `app/<service>/`, and use a unique migration table name so its migrations do not conflict with others.
- A service may also keep its own handlers, task executors, and API definitions under `app/<service>/`.
- Use the shared `schemas` configuration in `anclax.yaml` to reuse schema definitions across services.
