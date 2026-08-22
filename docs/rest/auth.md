# Authenticating a request

sqlb answers **who is calling** and hands that answer to the query layer. It
does not decide what the caller may see — that is a
[hook](../queries/hooks.md), and it is application code, because "may see" is a
sentence about your domain and not about your token format.

The whole seam is four pieces in the root package, with no dependency beyond
the standard library:

| | |
|---|---|
| `CredentialExtractor` | `func(*http.Request) (cred string, ok bool)` — pull the raw credential out of the request. `sqlb.BearerToken` is the built-in one; a cookie-based flow supplies its own |
| `Verifier[T]` | `Verify(ctx, cred) (T, error)` — turn a credential into *your* principal type. sqlb defines no canonical claims struct |
| `Middleware[T]` | ordinary `net/http` middleware: extract → verify → `WithPrincipal` → `next`. No credential or a rejected one answers 401 and never calls `next` |
| `WithPrincipal` / `PrincipalFrom[T]` | the context contract the hooks read |

## The whole wiring

```go
type Claims struct {
    Subject   string
    Workspace string
    Role      string
}

verify := sqlb.VerifierFunc[Claims](func(ctx context.Context, cred string) (Claims, error) {
    t, err := tokens.Resolve(ctx, cred)   // your provider, your token service
    if err != nil {
        return Claims{}, err
    }
    return Claims{Subject: t.UserID}, nil
})

protected := sqlb.Middleware[Claims](verify, sqlb.BearerToken)
mux.Handle("/api/", protected(apiHandler))
```

`VerifierFunc` is the `http.HandlerFunc` shape: most verifiers are one function
closing over one dependency, and declaring a struct with a single pass-through
method to satisfy a one-method interface is a type that means nothing. A
verifier with real state — `example/auth-workos`, which refreshes a JWKS in the
background — is still better as a named type, which is why the interface is
what `Middleware` takes.

From there the principal is a hook's to read:

```go
sqlb.On[Card](reg).BeforeQuery(func(ctx context.Context, q *sqlb.Builder[Card]) error {
    c, ok := sqlb.PrincipalFrom[Claims](ctx)
    if !ok {
        return errors.New("no principal on this context")   // fail closed
    }
    q.Where(sqlb.F("workspace_id").Eq(c.Workspace))
    return nil
})
```

**Fail closed on that `ok`.** "Nothing stored" and "a principal of a different
type" deliberately collapse to one `false`, so a hook cannot couple itself to
which middleware ran — and treating `false` as "no restriction" turns every path
that forgot to authenticate into a read across every tenant. `PrincipalFrom`'s
doc comment says the same thing, and so does
[Capabilities](../schema/capabilities.md#scoped-so-the-missing-hook-is-caught),
which is the check that refuses to mount a `Scoped` model with no hook at all.

## A provider outage is not a bad credential

`Verify` returning an error means 401 — the credential did not check out. That
is the wrong answer when the provider was simply unreachable: an operator pages
on 5xx and sees none, and a client that should back off retries instead.
Wrapping the failure says so:

```go
if errors.Is(err, context.DeadlineExceeded) {
    return Claims{}, sqlb.TransientError{Err: err}   // 500, not 401
}
```

It is opt-in, and a verifier with no network call to fail — local JWT
verification — never needs it, because every error it returns really is a 401.
By value is the recommended spelling; a pointer is recognised too, so the
habitual `&TransientError{…}` cannot silently downgrade a 500 into a 401.

## Identity is one stage; enrichment is another

`Verify` sees the credential and not the request, on purpose: a verifier that
reads headers is a verifier coupled to transport. But "who is calling" is rarely
the whole question. A multi-tenant application also needs *which workspace, and
what role there* — and that usually arrives in a header alongside the bearer
token, which `Verify` cannot see.

So it is a **second middleware**, chained after the first: read back the
principal that `Middleware` attached, resolve the rest, attach it again.

```go
identity := sqlb.Middleware[Claims](verify, sqlb.BearerToken)

enrich := func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        c, ok := sqlb.PrincipalFrom[Claims](r.Context())
        if !ok {
            http.Error(w, "no principal", http.StatusUnauthorized)
            return
        }
        ws := r.Header.Get("X-Workspace-Id")
        role, err := roleInWorkspace(r.Context(), sys, c.Subject, ws)
        if err != nil {
            http.Error(w, "not a member of that workspace", http.StatusForbidden)
            return
        }
        c.Workspace, c.Role = ws, role
        next.ServeHTTP(w, r.WithContext(sqlb.WithPrincipal(r.Context(), c)))
    })
}

mux.Handle("/api/", identity(enrich(apiHandler)))
```

Three things make this the shape to copy rather than one of several:

- **The order is the security property.** Enrichment runs only behind a verified
  credential, so a forged `X-Workspace-Id` reaches a lookup keyed on a subject
  that came from `Verify` — not from the wire.
- **The refusal is a 403, not a 401.** The caller is who they say they are and
  is not a member; collapsing that into 401 tells a client to re-authenticate,
  which will not help.
- **A hook cannot tell the two stages apart.** It reads one principal type, so
  the staging stays an application concern and hooks written against `Claims`
  keep working when the second stage changes.

The membership lookup runs on a handle that is *not* scoped by membership —
otherwise it asks the question it is trying to answer. That is the case for a
[second registry](../queries/hooks.md#the-second-registry-is-not-only-for-tests):
there is no principal yet, which is exactly what it is for.

`ExampleMiddleware_enrichment` in the root package is this, runnable, with the
three request outcomes asserted.

## What sqlb does not do

- **Authorization.** The seam carries identity; the hooks confine rows. See
  [Hooks](../queries/hooks.md) and, for the surface that deliberately sees
  across tenants, [a cross-tenant admin surface](admin.md).
- **A public/protected path list.** `Middleware` protects whatever subtree it is
  mounted on. `example/tasks/auth` wraps it in a deny-by-default allow-list —
  protect everything except named public paths — so a new private route is
  protected by having been forgotten rather than exposed by it.
- **Multi-round-trip flows.** `Verify(ctx, cred) (T, error)` is one credential,
  one call. An application needing more composes its own `Verifier[T]` around it.
- **Declaring the scheme in OpenAPI.** That is `rest.Options.Security`, which
  documents the requirement in the generated spec and enforces nothing —
  enforcement is the middleware. See [Mounting resources](README.md).

## The adapters, and why they are examples

A provider adapter lives in `example/`, never in the root module, so its
dependencies stay out of sqlb's `go.mod`
([the driver is a dependency](../architecture.md#the-driver-is-a-dependency)):

- [`example/tasks/auth`](../../example/tasks/auth) — HS256 JWT and password
  hashing written out rather than imported, so the whole path from
  `Authorization` header to `WHERE` clause is readable in one sitting. It pins
  the algorithm before checking the signature, compares in constant time, and
  requires an expiry — the three mistakes that turn a JWT implementation into a
  bypass.
- [`example/fxapp/access`](../../example/fxapp/access) — a shared-secret key
  naming which tenant a request speaks for. The wiring half only: no users, no
  sessions, no revocation.
- [`example/auth-workos`](../../example/auth-workos) — WorkOS AuthKit JWTs
  verified against the per-client JWKS endpoint, its own module so the WorkOS
  and JWT dependencies reach nothing else.

## Next

- [Hooks](../queries/hooks.md) — what the principal is *for*, including the
  worker identity that releases one named rule
- [Capabilities](../schema/capabilities.md) — `Scoped`, and the mount that
  refuses without a hook
- [A cross-tenant admin surface](admin.md) — the release, and the route guard it
  does not give you
- [Where domain logic goes](../concepts/domain-logic.md) — hook, constraint, or
  handler
