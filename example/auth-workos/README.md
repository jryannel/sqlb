# example/auth-workos — a sqlb.Verifier[T] adapter for WorkOS AuthKit

A `sqlb.Verifier[T]` (see `auth.go` in the sqlb module) that verifies WorkOS
AuthKit access tokens — JWTs WorkOS signs and hands to a client after login,
presented back to an application as a bearer credential — against WorkOS's
per-client JWKS endpoint, and maps a verified token to the application's own
principal type. It plugs into `sqlb.Middleware[T]` exactly like this
repository's other worked auth examples: `example/tasks/auth`'s hand-rolled
HS256 JWT and `example/fxapp/access`'s shared-secret bearer keys. This one
verifies tokens WorkOS minted rather than tokens the application mints
itself, which is why it needs a real JWT/JWKS library
([`golang-jwt/jwt/v5`](https://github.com/golang-jwt/jwt),
[`MicahParks/keyfunc/v3`](https://github.com/MicahParks/keyfunc)) instead of
the ~100 lines `example/tasks/auth` writes by hand for one fixed HS256
secret.

## Why its own module

sqlb core is dependency-locked to pgx only; this package needs WorkOS's SDK
plus a JWT/JWKS library, none of which sqlb's own `go.mod` may carry. It
lives under `example/` with its own `go.mod` for exactly that reason — see
[docs/architecture.md's "A Verifier composes with the principal
seam"](../../docs/architecture.md#a-verifier-composes-with-the-principal-seam).

## Using it

```go
type Principal struct {
	UserID string
	OrgID  string
	Role   string
}

func mapClaims(c authworkos.Claims) Principal {
	return Principal{UserID: c.Subject, OrgID: c.OrgID, Role: c.Role}
}

// ctx should be an application's own root context, not a per-request one —
// keyfunc ties its background key-set refresh to it.
verifier, err := authworkos.New(ctx, workosClientID, mapClaims)
if err != nil {
	log.Fatal(err) // WorkOS's JWKS endpoint is fetched here, at startup
}

mw := sqlb.Middleware[Principal](verifier, sqlb.BearerToken)
mux.Handle("/", mw(handler))
```

A verified request carries its `Principal` through `sqlb.PrincipalFrom[Principal](ctx)`,
same as every other adapter behind this seam.

`New` fetches the client's JWKS synchronously and fails if that fetch fails,
so a WorkOS outage surfaces as a startup error rather than a per-request
one — see `workos.go`'s package doc comment ("Why Verify never returns
sqlb.TransientError") for why `Verify` itself never distinguishes an
outage from a rejected token.

## Testing

```bash
mise run test-auth-workos
```

Database-free: the suite verifies JWTs against an in-memory or
httptest-served JWKS built from a freshly generated RSA key, never a live
WorkOS endpoint or a database.
