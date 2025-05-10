# myexampleapp 

This project is initialized by `anchor init` command.

## File structure

`api`: Define API spec, the controller layer of your application.

`internal/handler`: Implement the business logic of the HTTP API.

`internal/asynctask`: Implement the business logic of the task handler.

`config`: Define a global config. The config can be passed via config file or environment variables.

`zcore`: The core of the anchor application, these files should not be edited. It will be updated by `anchor update`.

`zgen`: The generated code by tool chains, these files should not be edited. It will be updated by `anchor gen`.

## Quick Test
```
docker compose up 
curl http://localhost:2910/api/v1/counter

curl -X POST http://localhost:2910/api/v1/auth/sign-in -H "Content-Type: application/json" -d '{"name": "test", "password": "test"}'

curl -X POST http://localhost:2910/api/v1/counter -H "Content-Type: application/json" -H "Authorization: your_access_token"

curl http://localhost:2910/api/v1/counter
```
