# Auth support in sqlb

sqlb ships a minimal, stdlib-only **authentication seam**, not a full auth
system. It answers "who is calling" and hands that identity to the query
layer; deciding what that identity may see is left entirely to application
hook code. This is deliberate — see the "Where safety lives" and "A Verifier
composes with the principal seam" decisions in `docs/architecture.md`.

## The seam (module root: `auth.go`, `principal.go`)

| Piece | What it does |
|---|---|
| `principalKey` / `WithPrincipal(ctx, p)` / `PrincipalFrom[T](ctx)` | The context contract. Stores/reads a principal of *any application-chosen type* — a tenant id, a claims struct, a user record. sqlb never inspects it, only carries it. |
| `CredentialExtractor` | `func(r *http.Request) (cred string, ok bool)` — pulls a raw credential out of a request. |
| `BearerToken` | The built-in `Authorization: Bearer <token>` (RFC 6750) extractor. Cookie-based flows (WorkOS AuthKit, Clerk's hosted UI) supply their own. |
| `Verifier[T]` | `Verify(ctx, cred string) (T, error)` — generic over the app's own principal type. sqlb defines no canonical `Principal` struct; a provider adapter maps its claims into whatever type the app's hooks already read. |
| `TransientError{Err error}` | Opt-in marker a `Verify` returns **by value** to mean "provider unreachable," so `Middleware` answers 500 instead of 401. A `Verifier` with no network call (e.g. local JWT) never needs it — every failure is correctly a 401. |
| `Middleware[T](verifier, extractor)` | Ordinary `net/http` middleware: extract → verify → `WithPrincipal` → call `next`. Missing/rejected credential → 401 (RFC 9457 problem+json), transient failure → 500. Composes with any router — nothing Huma-specific. |

Two failure modes at `PrincipalFrom[T]` — "nothing stored" and "wrong
type" — deliberately collapse to one `false`. Hooks must fail closed on
that (deny), never treat it as "no restriction."

## What's *not* in core

- **Authorization.** sqlb verifies identity; a `BeforeQuery` hook reading
  `PrincipalFrom[T]` is what actually scopes rows to a tenant/user.
- **A path allow-list.** `Middleware[T]` protects whatever subtree it's
  mounted on; core stays silent on public-vs-protected routing policy.
- **Multi-factor / multi-round-trip verification.** `Verify(ctx, cred) (T, error)`
  is a one-credential, one-call shape by design; an app needing more composes
  its own `Verifier[T]` around it.

## `rest` package integration

`rest` never enforces auth. `Options.Security []map[string][]string` (and
the matching field on `events.go`'s SSE options) only documents the OpenAPI
security requirement (e.g. `{"bearerAuth": {}}`) so generated docs/spec show
it. Enforcement is `sqlb.Middleware[T]` mounted on the router in front of
the mount — the same "app owns its middleware stack" philosophy as
`rest.Resource` taking a `huma.API` rather than owning a router.

## Worked examples (each its own Go module, never imported into sqlb core)

Provider/scheme adapters stay `example/`-only so their dependencies never
reach sqlb's pgx-only `go.mod` (per the "The driver is a dependency"
decision).

- **`example/tasks/auth/`** — hand-rolled HS256 JWT + password hashing,
  written out in ~100 lines rather than imported, specifically so a reader
  can trace the whole path from `Authorization` header to `WHERE` clause.
  Not a general-purpose JWT library; explicitly guards the three mistakes
  that turn a JWT implementation into a bypass (algorithm pinned before the
  signature is checked, constant-time comparison, mandatory expiry). Its
  `Middleware` uses a **deny-by-default allow-list** (protect everything
  except named public paths) rather than opt-in protection, so a forgotten
  new private route stays protected by default.
- **`example/fxapp/access/`** — a shared-secret bearer key identifying which
  "space" (tenant) a request speaks for, wired through the same principal
  seam. Explicitly **not** an authentication system (no users, sessions, or
  revocation) — it's the wiring half; `example/tasks/auth` is where real
  identity verification lives.
- **`example/auth-workos/`** — verifies WorkOS AuthKit JWTs against WorkOS's
  per-client JWKS endpoint (`golang-jwt/jwt/v5` + `MicahParks/keyfunc/v3`),
  checking issuer, expiry, and client id, then mapping claims (`Subject`,
  `SessionID`, `ClientID`, `OrgID`, `Role`/`Roles`, `Permissions`) into the
  app's principal type via a caller-supplied mapper. Never returns
  `TransientError`: the JWT/JWKS libraries don't reliably distinguish "WorkOS
  is down" from "unrecognized key," so every per-request rejection is a
  plain 401; a WorkOS-unreachable failure only surfaces once, as a startup
  error from `New`, which fetches the JWKS synchronously at construction.

## Why it's shaped this way

From `docs/architecture.md`, "A Verifier composes with the principal seam":
four applications (WorkOS, Clerk, Zitadel, self-hosted JWT) were each about
to hand-write the same extract → verify → `WithPrincipal` middleware
independently. The seam was promoted into core — with **zero new
dependencies** — once a second/third/fourth author proved the pattern
needed restating rather than reinventing. `Verifier[T]` stays generic
(rather than sqlb owning a canonical principal shape) for the same reason
`PrincipalFrom[T]` does. Widening the `Verifier`/`Middleware` contract
later is considered free; narrowing it is no longer free now that
`example/auth-workos` depends on the current signature.
