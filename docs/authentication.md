# Authentication

Anclax ships with a macaroon-based authentication foundation. You can use the built-in simple username/password APIs for quick starts, or keep those disabled and build your own auth APIs on top of the same service and token primitives.

This guide covers:
- runtime config for built-in auth behavior
- the default simple auth endpoints
- how Anclax macaroon bearer tokens work
- how to implement your own auth APIs using Anclax service/auth interfaces

## Runtime config

Relevant config lives under `anclax` in your app config.

Example:

```yaml
anclax:
  enableSimpleAuth: true
  auth:
    accessexp: 15m
    refreshexp: 24h
    singlesession: true
  testaccount:
    password: test
```

Key fields:

- `enableSimpleAuth`:
  - default: `false`
  - enables the built-in `POST /auth/sign-in` and `POST /auth/sign-up` endpoints
- `disableDefaultSignUp`:
  - default: `false`
  - deprecated compatibility flag
  - if `true`, built-in `POST /auth/sign-up` stays disabled even when `enableSimpleAuth` is `true`
- `auth.accessexp`:
  - access token lifetime
- `auth.refreshexp`:
  - refresh token lifetime
- `auth.singlesession`:
  - if `true`, signing in invalidates the user's previous tokens
- `testaccount.password`:
  - optional bootstrap test user password for the built-in `test` account

Implementation references:
- config: `pkg/config/config.go`
- auth setup: `pkg/auth/auth.go`
- test account bootstrap: `pkg/app/app.go`

## Built-in simple auth APIs

Anclax provides a small default auth surface in `api/openapi` and `pkg/controller/controller.go`.

| Endpoint | Default | Notes | Main implementation |
|---|---|---|---|
| `POST /api/v1/auth/sign-in` | disabled | enabled only when `enableSimpleAuth: true` | `service.SignInWithPassword` |
| `POST /api/v1/auth/sign-up` | disabled | enabled only when `enableSimpleAuth: true`; also blocked by `disableDefaultSignUp: true` | `service.CreateNewUser` + `service.SignIn` |
| `POST /api/v1/auth/refresh` | enabled | refreshes access/refresh tokens using a refresh token | `service.RefreshToken` |
| `POST /api/v1/auth/sign-out` | enabled | invalidates all tokens for the authenticated user | `auth.InvalidateUserTokens` |

These endpoints are best treated as reference/default APIs. For production applications with custom signup rules, external identity providers, invitation flows, OTP, SSO, or custom response shapes, implement your own auth endpoints and reuse the same service/auth building blocks.

## Macaroon bearer tokens

Anclax uses bearer tokens backed by macaroons.

OpenAPI security scheme example:

```yaml
components:
  securitySchemes:
    BearerAuth:
      type: http
      scheme: bearer
      bearerFormat: macaroon
```

Protected operation example:

```yaml
paths:
  /orgs:
    get:
      operationId: listOrgs
      security:
        - BearerAuth: []
```

### Validation flow

At request time, your validator should delegate to Anclax auth:

```go
func (v *Validator) AuthFunc(c fiber.Ctx) error {
	return v.auth.Authfunc(c)
}

func (v *Validator) PreValidate(c fiber.Ctx) error {
	return v.auth.Authfunc(c)
}
```

Reference: `pkg/controller/validator.go`

### What the token carries

The built-in auth flow uses two caveat types:

- `user_context`
  - stored on access tokens
  - writes `userID` and `orgID` into the Fiber context
- `refresh_only`
  - stored on refresh tokens
  - allows the token to be used only on `POST .../auth/refresh`

Reference: `pkg/auth/caveats.go`

### Reading auth context in handlers/controllers

After token validation, use helpers from `pkg/auth`:

```go
userID, err := auth.GetUserID(c)
orgID, err := auth.GetOrgID(c)
token, err := auth.GetToken(c)
```

### Token issuance primitives

Use these depending on how much control you need:

- `service.SignIn(ctx, userID)`
  - easiest way to mint standard credentials for an existing local user
- `service.SignInWithPassword(ctx, apigen.SignInRequest{...})`
  - username/password verification + credential issuance
- `service.RefreshToken(ctx, refreshToken)`
  - token rotation using the standard refresh flow
- `auth.CreateUserTokens(ctx, userID, orgID, caveats...)`
  - create an access token and refresh token directly
- `auth.CreateToken(...)` / `auth.CreateRefreshToken(...)`
  - lower-level token control for advanced cases
- `auth.InvalidateUserTokens(ctx, userID)` / `auth.InvalidateToken(ctx, keyID)`
  - revoke tokens

References:
- service auth logic: `pkg/service/auth_service.go`
- token primitives: `pkg/auth/auth.go`

## Implementing your own auth APIs

If your app needs custom auth endpoints, keep the built-in simple auth APIs disabled and expose your own operations in your app's OpenAPI spec.

Typical workflow:
1. Add your auth endpoints to `api/openapi`.
2. Run `anclax gen`.
3. Implement handlers/controllers against generated `apigen` request/response types.
4. Reuse `service.ServiceInterface` and/or `auth.AuthInterface` instead of reimplementing password hashing or token creation in handlers.

### Pattern 1: custom username/password login

If you just want different routes or response shapes, reuse `service.SignInWithPassword`.

```go
type AuthHandler struct {
	svc service.ServiceInterface
}

func (h *AuthHandler) Login(c fiber.Ctx) error {
	var req apigen.SignInRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	creds, err := h.svc.SignInWithPassword(c.Context(), req)
	if err != nil {
		return err
	}

	return c.JSON(creds)
}
```

### Pattern 2: custom sign-up / provisioning

Use `service.CreateNewUser` to create the local user and default org, then `service.SignIn` to issue standard credentials.

```go
userMeta, err := h.svc.CreateNewUser(c.Context(), req.Name, req.Password)
if err != nil {
	return err
}

creds, err := h.svc.SignIn(c.Context(), userMeta.UserID)
if err != nil {
	return err
}

return c.Status(fiber.StatusCreated).JSON(creds)
```

Use `CreateNewUserWithTx` if signup must happen inside a larger transaction.

### Pattern 3: external IdP / SSO / passwordless

If you authenticate the user outside Anclax, verify that external credential first, then map it to a local Anclax user and issue Anclax tokens.

Common pattern:
1. Verify the external token/assertion yourself.
2. Find the local user with `service.GetUserByUserName` or another app-specific lookup.
3. Provision the local user with `service.CreateNewUser` if needed.
4. Issue credentials with either:
   - `service.SignIn(ctx, userID)` for standard tokens, or
   - `auth.CreateUserTokens(ctx, userID, orgID, caveats...)` if you need custom caveats

Example:

```go
userMeta, err := h.svc.GetUserByUserName(c.Context(), externalIdentity)
if err != nil {
	if !errors.Is(err, service.ErrUserNotFound) {
		return err
	}

	userMeta, err = h.svc.CreateNewUser(c.Context(), externalIdentity, randomPassword)
	if err != nil {
		return err
	}
}

creds, err := h.svc.SignIn(c.Context(), userMeta.UserID)
if err != nil {
	return err
}

return c.JSON(creds)
```

If you bypass `service.SignIn` and call `auth.CreateUserTokens` directly, return the tokens in your own response or adapt them into `apigen.Credentials`.

### Pattern 4: custom refresh and logout APIs

You can expose custom paths while still using the built-in logic.

Refresh:

```go
creds, err := h.svc.RefreshToken(c.Context(), req.RefreshToken)
```

Logout / revoke all current-user tokens:

```go
userID, err := auth.GetUserID(c)
if err != nil {
	return c.SendStatus(fiber.StatusUnauthorized)
}
return h.auth.InvalidateUserTokens(c.Context(), userID)
```

## Wiring service/auth into your own app handlers

The default Anclax application already owns `service.ServiceInterface` and `auth.AuthInterface`. For app-specific handlers, expose them through small injectors and Wire.

Example injector pattern:

```go
func InjectAuth(anclaxApp *anclax_app.Application) auth.AuthInterface {
	return anclaxApp.GetAuth()
}

func InjectService(anclaxApp *anclax_app.Application) service.ServiceInterface {
	return anclaxApp.GetService()
}
```

Then include those providers in your `wire.Build(...)` set for your app-specific auth handlers.

Reference for the existing auth injector pattern: `examples/simple/app/injection.go`

## Recommendations

- Keep built-in simple auth disabled unless you explicitly want the default username/password endpoints.
- Use service methods for user lifecycle and standard credential issuance.
- Use `auth.AuthInterface` when you need custom token issuance or revocation behavior.
- Treat macaroons as the canonical bearer token format in your OpenAPI spec and validator implementation.
- Keep controllers/handlers thin; put user-creation and sign-in business rules in the service layer.
