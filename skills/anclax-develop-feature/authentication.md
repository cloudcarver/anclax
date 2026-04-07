# Authentication

Use Anclax service/auth primitives instead of reimplementing password hashing, token issuance, or refresh logic in handlers.

## What Anclax provides

- Macaroon-based bearer tokens via `pkg/auth` and `pkg/macaroons`
- Optional built-in username/password endpoints in `api/openapi`
- Service-layer helpers for user creation, sign-in, refresh, and password updates in `pkg/service`
- Validator integration for protected routes via `AuthFunc` / `PreValidate`

## Runtime config

Relevant `pkg/config.Config` fields:

- `EnableSimpleAuth bool`
  - default false
  - enables built-in `POST /auth/sign-in` and `POST /auth/sign-up`
- `DisableDefaultSignUp bool`
  - deprecated compatibility flag
  - if true, built-in sign-up stays disabled even when simple auth is enabled
- `Auth.AccessExpiry`
- `Auth.RefreshExpiry`
- `Auth.SingleSession`
- `TestAccount.Password`

App config example:

```yaml
anclax:
  enableSimpleAuth: true
  auth:
    accessexp: 15m
    refreshexp: 24h
    singlesession: true
```

## Built-in auth endpoint behavior

Implementation lives in `pkg/controller/controller.go` and `pkg/service/auth_service.go`.

- `POST /auth/sign-in`
  - disabled unless `EnableSimpleAuth` is true
  - uses `service.SignInWithPassword`
- `POST /auth/sign-up`
  - disabled unless `EnableSimpleAuth` is true
  - also disabled when `DisableDefaultSignUp` is true
  - uses `service.CreateNewUser` then `service.SignIn`
- `POST /auth/refresh`
  - available for token refresh flows
  - uses `service.RefreshToken`
- `POST /auth/sign-out`
  - invalidates all tokens for the authenticated user

Treat sign-in/sign-up as reference endpoints. Prefer custom APIs when product requirements differ.

## Macaroon token reference

OpenAPI security scheme:

```yaml
components:
  securitySchemes:
    BearerAuth:
      type: http
      scheme: bearer
      bearerFormat: macaroon
```

Validator pattern:

```go
func (v *Validator) AuthFunc(c fiber.Ctx) error {
	return v.auth.Authfunc(c)
}

func (v *Validator) PreValidate(c fiber.Ctx) error {
	return v.auth.Authfunc(c)
}
```

Context helpers after validation:

```go
userID, err := auth.GetUserID(c)
orgID, err := auth.GetOrgID(c)
token, err := auth.GetToken(c)
```

Token caveats to know:
- `user_context`: stores user/org context on access tokens
- `refresh_only`: restricts refresh tokens to `POST .../auth/refresh`

## Preferred primitives for custom auth APIs

Use `service.ServiceInterface` first:

- `CreateNewUser` / `CreateNewUserWithTx`
- `GetUserByUserName`
- `IsUsernameExists`
- `SignIn`
- `SignInWithPassword`
- `RefreshToken`
- `UpdateUserPassword`

Use `auth.AuthInterface` for advanced token control:

- `CreateUserTokens`
- `CreateToken`
- `CreateRefreshToken`
- `InvalidateUserTokens`
- `InvalidateToken`

## Custom auth API patterns

### Custom login route with standard password auth

- Add your route to OpenAPI
- Run `anclax gen`
- In the handler, bind the generated request type and call `service.SignInWithPassword`

### Custom signup route

- Use `service.CreateNewUser` for local provisioning
- Then call `service.SignIn` to mint standard credentials
- Use `CreateNewUserWithTx` if signup participates in a larger transaction

### External IdP / SSO / passwordless

- Verify the external credential in your handler/service
- Map that identity to a local user
- Provision with `CreateNewUser` if necessary
- Issue standard tokens with `service.SignIn`
- If you need custom caveats, inject `auth.AuthInterface` and call `CreateUserTokens`

### Custom logout / revoke

- Read the current user with `auth.GetUserID(c)`
- Revoke with `auth.InvalidateUserTokens(ctx, userID)`

## Wiring into app-specific handlers

The framework app exposes auth/service through `Application` getters.

Existing example:
- `examples/simple/app/injection.go` exposes `InjectAuth`

If your custom handlers need service-level auth helpers, add a similar injector:

```go
func InjectService(anclaxApp *anclax_app.Application) service.ServiceInterface {
	return anclaxApp.GetService()
}
```

Then include it in your Wire provider set.

## Workflow

1. Update the OpenAPI spec for your auth endpoints.
2. Run `anclax gen`.
3. Implement handlers against generated `apigen` types.
4. Reuse `service`/`auth` interfaces instead of duplicating auth logic.
5. Protect routes with `BearerAuth` and validator hooks.
6. Add unit tests for service-layer auth logic and handler error mapping.

For a longer user-facing reference, see `docs/authentication.md`.
