# Pluggable auth for sqlb

Status: proposed
Date: 2026-08-15

## Problem

Applications built on sqlb need to authenticate requests against different
identity providers — WorkOS, Clerk, Zitadel, or a self-hosted JWT/session
scheme — and sqlb should not force a choice among them. Two examples already
prove this works informally: `example/tasks/auth/` (hand-rolled JWT) and
`example/fxapp/access/` (shared-secret bearer keys) both funnel into the same
engine-level seam, `sqlb.WithPrincipal`/`sqlb.PrincipalFrom` (`principal.go`).
That seam is deliberately untyped — the app names its own principal type —
and per [ADR-0044](../../adr/0044-the-container-is-an-adapter.md)
sqlb publishes seams and lets assembly stay app-owned copy-paste.

What's missing is a common shape for "verify a credential, then call
`WithPrincipal`" so that four concrete near-term integrations (WorkOS, Clerk,
Zitadel, self-hosted) don't each reinvent the same middleware skeleton.
ADR-0044 names its own reconsideration trigger: *"a second author writes an
sqlb-\* module."* Four simultaneous authors asking for the same shape is that
trigger being pulled for auth specifically.

## Scope

**In scope:**
- A generic `Verifier[T]` interface and `Middleware[T]` wrapper, published in
  sqlb core, stdlib-only.
- Credential extraction (bearer header, or app-supplied alternative such as a
  cookie) as a separate, composable piece from verification.
- Worked examples wiring WorkOS, Clerk, and Zitadel through this shape, plus
  promoting `example/tasks/auth/jwt.go` to the reference self-hosted example.
- A worked pattern for composing multiple principal types in one app (e.g.
  a user realm and a superadmin realm, each with its own provider) — see
  "Composing multiple principal types" below. This needs no new primitive;
  it is one `Middleware[T]` per realm, mounted on separate route groups.
- An ADR recording this decision, extending ADR-0044 rather than standing
  alone.

**Explicitly out of scope (not precluded, just not this spec):**
- Schema/codegen changes — no `RequireAuth` tag, no OpenAPI security
  schemes, no client-SDK auth awareness. This is a REST-layer-only design;
  the schema DSL and generated clients stay auth-agnostic, matching how
  `rest.Resource` already treats auth as the app's concern via hooks reading
  `PrincipalFrom[T]`.
- Multi-provider chaining *within a single realm* (e.g. "accept WorkOS OR an
  API key for the same user identity" in one app). An app needing that
  composes its own `Verifier[T]`; sqlb does not need to model composition.
  This is distinct from multiple realms each with their own single
  provider, which is in scope — see below.
- A default, bundled auth service shipped with sqlb (the "Supabase way" —
  sqlb owning its own GoTrue-equivalent). This design's `Verifier[T]` seam
  does not preclude that later, but building it is a separate spec.
- Pluggable storage. Tracked as a separate research note, not designed here.

## Design

### `Verifier[T]` and `Middleware[T]`

New file `auth.go` at the module root, alongside `principal.go`, with zero
new dependencies (stdlib only — `context`, `net/http`):

```go
// Verifier checks a credential and returns the application's own principal
// type. T is the same type the application later reads back with
// sqlb.PrincipalFrom[T] — Verifier does not introduce a new principal shape,
// it produces the one the application already owns.
type Verifier[T any] interface {
	Verify(ctx context.Context, cred string) (T, error)
}

// CredentialExtractor pulls a raw credential (a bearer token, a cookie
// value, ...) out of an inbound request. Returns ok=false when no
// credential is present.
type CredentialExtractor func(*http.Request) (cred string, ok bool)

// BearerToken extracts a credential from the Authorization: Bearer <token>
// header. The default extractor; most providers that issue access tokens
// (Zitadel OIDC, self-hosted JWT, WorkOS/Clerk API-mode) use it unchanged.
func BearerToken(r *http.Request) (string, bool)

// Middleware wraps any Verifier[T] as net/http middleware. On a missing or
// invalid credential it responds 401 and calls next no further. On a
// Verifier error that is not itself an authentication failure (a network
// error reaching the provider, a provider outage) it responds 500 instead
// of 401 — "the provider is down" must stay distinguishable from "the
// token is bad" for both operators and API consumers. On success it calls
// sqlb.WithPrincipal(ctx, principal) and continues the chain unchanged.
func Middleware[T any](v Verifier[T], extract CredentialExtractor) func(http.Handler) http.Handler
```

The 401-vs-500 split needs the `Verifier` to distinguish "credential is
invalid" from "I couldn't check." `Verify` returning a plain `error` doesn't
carry that distinction by default, so `Middleware` treats any `Verify` error
as invalid-credential (401) *unless* the error implements a small sentinel
the app or adapter can opt into:

```go
// TransientError marks a Verify failure as not-a-verdict-on-the-credential
// (a network error, a provider 5xx, a timeout) so Middleware responds 500
// instead of 401. Verifiers that can distinguish "the provider told me no"
// from "I couldn't reach the provider" should wrap the latter in this.
type TransientError struct{ Err error }

func (e TransientError) Error() string { return e.Err.Error() }
func (e TransientError) Unwrap() error { return e.Err }
```

This stays opt-in: a `Verifier` that never distinguishes the two cases (e.g.
local JWT verification, which has no network call to fail) simply never
returns `TransientError`, and every failure is a 401 — correct for that
adapter's failure modes.

### Why split extraction from verification

WorkOS's AuthKit and Clerk's hosted UI both commonly hand back session state
via a cookie for browser-driven flows, while Zitadel (OIDC access tokens)
and self-hosted JWT are bearer-token-shaped. Hardcoding bearer-header
extraction into `Middleware` would make it wrong for two of the four named
targets. `CredentialExtractor` is a plain function type so an app wiring
Clerk's cookie-based session can pass its own extractor while everything
else about `Middleware[T]` — verify, distinguish 401/500, call
`WithPrincipal` — stays shared.

### Provider adapters stay worked examples, not published modules

sqlb core is dependency-locked to pgx only; `rest` additionally depends on
huma; nothing else may add a dependency, enforced both ways by
`mise run deps-check`. A WorkOS, Clerk, or Zitadel adapter needs that
provider's SDK (or an OIDC library), which must never reach sqlb's `go.mod`.

Each adapter therefore lives under `example/` as its own Go module:

```
example/
  auth-workos/    (own go.mod — workos-go dependency)
  auth-clerk/     (own go.mod — clerk-sdk-go dependency)
  auth-zitadel/   (own go.mod — zitadel oidc dependency)
  tasks/auth/     (existing hand-rolled JWT — becomes the
                   reference self-hosted example; not moved,
                   just documented as filling this role)
```

Each example implements `sqlb.Verifier[T]` for that provider (`Verify` calls
the provider's token-introspection or JWKS-verification path, maps the
provider's claims to the app's own principal type) and shows it wired into
`sqlb.Middleware[T]`. Nothing here is versioned or published as a
`go get`-able sqlb package — consistent with ADR-0044's "publish the seam,
copy the assembly," now exercised for auth specifically rather than for
container wiring.

### Error handling summary

| Condition | Response |
|---|---|
| No credential present (`extract` returns `ok=false`) | 401 |
| `Verify` returns a plain error | 401 |
| `Verify` returns a `TransientError`-wrapped error | 500 |
| `Verify` succeeds | `WithPrincipal` + continue chain |

### Testing

`Middleware[T]` gets unit tests in core against a fake `Verifier[T]`
covering: success, invalid credential (401), missing credential (401), and
`TransientError` (500) — the 401-vs-500 split is exactly the kind of guard
ADR-0016 asks to be proven both ways: written so it can be shown catching a
regression that collapses a provider outage into an indistinguishable 401.

Each example adapter gets a lightweight test against a fake HTTP response
standing in for the provider (a canned JWKS document, a canned
introspection response) — no live WorkOS/Clerk/Zitadel calls in CI, matching
how the rest of the repo keeps database-free tests database-free.

## Composing multiple principal types

Real apps built on this design need more than one auth realm in the same
process — a customer-facing surface and a separate admin surface, or three
or more distinct identities with different access shapes. `Verifier[T]` and
`Middleware[T]` already compose for this without any new primitive: each
realm is its own principal type `T`, its own `Verifier[T]`, and its own
`Middleware[T]` mounted on its own route group. `PrincipalFrom[T]` is keyed
by the type of `T`, not by name, so two realms in the same process never
collide, and `rest.Resource` already supports mounting the same or
different resources more than once with per-mount reachability
([ADR-0050](../../adr/0050-reachability-is-a-property-of-the-mount.md)).

**Two realms — customer users and superadmins**, each with their own
provider:

```go
type UserPrincipal struct{ ID, OrgID string }
type AdminPrincipal struct{ ID string; PlatformAdmin bool }

userMW := sqlb.Middleware[UserPrincipal](workosVerifier, sqlb.BearerToken)
adminMW := sqlb.Middleware[AdminPrincipal](internalJWTVerifier, sqlb.BearerToken)

api.UseOn("/api/*", userMW)
api.UseOn("/admin/*", adminMW)

// hooks on user-facing resources read PrincipalFrom[UserPrincipal];
// hooks on admin resources read PrincipalFrom[AdminPrincipal].
// Neither hook can accidentally read the other realm's principal —
// the type itself is the boundary.
```

**Three realms with overlapping access — users bound to one org, coaches
with their own auth and access to a set of *assigned* orgs, and superusers
managing the whole instance**:

```go
type UserPrincipal struct{ ID, OrgID string }
type CoachPrincipal struct{ ID string; AssignedOrgIDs []string }
type SuperuserPrincipal struct{ ID string }
```

This is still three `Verifier[T]`/`Middleware[T]` pairs on three mounts —
authentication is unchanged from the two-realm case above. What's new is
*authorization*: a coach's assigned-org set is not a single foreign key, so
confining a coach's reads/writes to it is a job for
`Hooks[T].BeforeQuery`/`BeforeCreate`/etc. reading `PrincipalFrom[CoachPrincipal]`
and filtering by `AssignedOrgIDs`, exactly as any other row-scoping hook
does today ([ADR-0008](../../adr/0008-hooks-as-domain-seam.md),
[ADR-0030](../../adr/0030-declared-scope-is-required.md)). A superuser mount
either uses a hook that scopes to nothing (unrestricted) or, if the same
resource is also mounted for users/coaches with a named scope, releases
that scope for the superuser mount specifically via
[ADR-0054](../../adr/0054-a-named-scope-is-releasable-at-the-mount.md)'s
`Unscoped` mechanism. None of this requires touching `Verifier[T]` or
`Middleware[T]` — it is the existing hooks seam doing what it already does,
composed with three independently-authenticated realms rather than one.

**What this does not need:** a single `Verifier[T]` that tries multiple
providers for one principal type (that remains out of scope, see above); a
registry or naming scheme for realms (the Go type system already
distinguishes them); or any change to how `rest.Resource` mounts work
(ADR-0050 already covers per-mount reachability).

## Documentation

A new ADR (next available number, currently 0059) records this decision. It
extends ADR-0044 rather than standing alone: ADR-0044 already anticipated
and named the trigger ("a second author writes a module") this design acts
on, so the new ADR should say so explicitly rather than re-argue the
seam-vs-assembly split from scratch.

`docs/architecture.md`'s "Where safety lives" section gains one row noting
that authentication is a REST-layer concern (`sqlb.Middleware[T]`) separate
from authorization/row-scoping (`Hooks[T]`, unchanged) — the two seams
compose but are not the same mechanism, and that distinction is currently
implicit rather than stated.

## Non-goals, restated

This spec deliberately does not design:
- A default/bundled sqlb auth service (the "Supabase way"). The `Verifier[T]`
  seam this spec adds does not block that later — a bundled auth service
  would itself just be another `Verifier[T]` implementation — but scoping
  and designing it is separate, larger work (a service, not a middleware
  shim) and belongs in its own spec once this seam has real adapters using
  it.
- Pluggable storage (blob/file storage, e.g. an S3-compatible backend such
  as rustfs). Captured as a research note below for a future spec.

## Research note: pluggable storage (not designed here)

No blob/file storage code exists anywhere in sqlb today — no `Storage`,
`Blob`, or file-shaped column type in `schema/`, no example using one. This
is genuinely greenfield, unlike auth, which had an existing seam to extend.
Worth knowing before that design starts:

- The user's concrete target backend is rustfs (S3-compatible), which
  suggests an S3-compatible-API abstraction rather than a Postgres large
  object or bytea-column approach — consistent with Postgres staying the
  system of record for structured data only ([ADR-0001](../../adr/0001-postgres-only.md)
  scopes Postgres-only to the *database*, not to blob storage, so this does
  not conflict with it).
- `Executor` (`exec.go:34-37`) is the seam any storage design touching query
  execution would need to respect if it wants to record file metadata rows
  (e.g. a `files` table tracking key, size, content-type) via sqlb itself.
- ADR-0043 (declared actions — custom verbs like `POST /{id}/<verb>` with
  generated envelope/routing/OpenAPI/client scaffolding) is a plausible
  template for how a schema-declared "upload" action could generate its
  request/response shape while the actual storage-backend call stays plain
  Go — worth reading in full before that spec starts.
- Whether storage follows the same "seam in core, adapters as examples"
  split this auth spec lands on is an open question for that spec, not
  answered here.
