# example/tasks — a multi-tenant task manager

The larger worked example: six tables, a workspace boundary that has to hold,
and JWT authentication. `example/blog` shows the shortest path from a schema to
a server; this one shows what the same machinery looks like once an application
has a real shape.

It is a module of its own, like `pgtest`, so its dependencies — a Postgres
driver, goose, testcontainers — cost the engine nothing. `mise run deps-check`
still reports **standard library only**, because a nested module is invisible to
the root module's package list by construction rather than by exemption.

## Running it

```bash
docker run --rm -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:18
```

```bash
export TASKS_DATABASE_URL='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable'
export TASKS_JWT_SECRET="$(head -c 32 /dev/urandom | base64)"
go run ./cmd/server
```

Then <http://localhost:8080/docs>. The migrations apply at startup, so an empty
database is enough.

```bash
curl -X POST localhost:8080/auth/register -H 'content-type: application/json' \
  -d '{"name":"Ada","email":"ada@example.com","password":"correct-horse-battery-staple","workspace":"Acme"}'
```

That returns a bearer token. Everything else needs it:

```bash
curl localhost:8080/tasks?priority=in.high,urgent&sort=-due_at -H "authorization: Bearer $TOKEN"
```

**Postgres 18 or newer.** `cmd/migrate` passes `migrate.MinPostgres(18)`, so
UUIDv7 keys use the built-in `uuidv7()` and the DDL applies to a stock install
with no extension. On 17 or older, change that call to `schema.GenUUIDv4` in the
schema or install [`pg_uuidv7`](https://github.com/fboulnois/pg_uuidv7).

## What to read, and in what order

| | |
|---|---|
| [`taskschema/schema.go`](taskschema/schema.go) | The source of truth. Six tables, and a comment on every decision that is not obvious. |
| [`app/hooks.go`](app/hooks.go) | The workspace boundary. One registration per model, and no handler that knows about tenants. |
| [`auth/jwt.go`](auth/jwt.go) | HS256 in the standard library, with the three checks that make a verifier safe rather than merely working. |
| [`app/auth_routes.go`](app/auth_routes.go) | Register and login: the endpoints that establish the identity everything else is scoped by. |
| [`app/hooks.go`](app/hooks.go) | Also where the comment invariant lives: two writes in one transaction, and `AfterCommit` for the side effect. |
| [`cmd/migrate/main.go`](cmd/migrate/main.go) | The generated baseline, plus three things the DSL cannot express. |
| [`app/server_test.go`](app/server_test.go) | Every claim above, asserted against a real Postgres. |

## The shape of it

```
register / login  ──▶  auth.Middleware  ──▶  claims in the context
                                                    │
                          generated handlers        │  BeforeQuery / BeforeCreate
                          hand-written handlers ────┴──▶  every query, scoped
```

`rest.Resource` mounts six tables in one call. None of those handlers mentions a
workspace, a token or a role — they call `sqlb.Query[T]`, and the hooks in
`app/hooks.go` add the predicate. That is the whole argument for hooks being the
domain seam: the scoping is written once and cannot be forgotten by a call site
that has not been written yet.

The middleware and the hooks are two independent checks. The middleware rejects
an unauthenticated request; the hooks refuse to build a query without claims. The
second exists because the interesting failures are the ones where the first is
bypassed — a background job, a test, a future gRPC surface. Neither ever falls
back to "no restriction", which is the shape most tenancy bugs take.

## Where the generated layer stops

Six endpoints are hand-written, in two groups, and each group marks a real
boundary rather than a gap in the generator:

- **`POST /auth/register`, `POST /auth/login`** — they establish the identity
  everything else is scoped by, so they run on a handle with no hooks attached.
  One deliberate exception, created in one place.
- **`DELETE /tasks/{id}`, `/lists/{id}`, `/comments/{id}`** — see below.

One used to be here and is not any more, which is the more interesting entry.
`POST /tasks/{id}/comments` existed because inserting a comment and
incrementing `tasks.comment_count` have to land together, and a generated
create under autocommit could not promise that. `rest` now wraps a generated
write in a transaction, so a hook receives a context carrying it — and the rule
moved to `BeforeCreate` and `AfterCreate` on the model. That is a stronger
guarantee than the endpoint gave, not merely a shorter one: the invariant now
holds for *every* path that creates a comment, including one written later by
someone who never read this file.

## Two things sqlb does not do that this example works around

Both are worth knowing before copying the schema.

**`schema.SoftDelete` adds a column and stops.** Nothing writes `deleted_at`,
nothing filters it out of reads, and the generated `DELETE` handler issues a real
`DELETE`. That is what its doc comment says, and `rest` has a test that fails if
the runtime ever starts reading the column instead — so this is settled
behaviour to build on, not a gap waiting to be closed. The tables that declare a
soft delete therefore do not expose `OpDelete`; the read hooks add
`deleted_at IS NULL`, and [`app/deletes.go`](app/deletes.go) serves `DELETE` as an
`UPDATE`. Both halves are a few lines and both are visible.

**Composite foreign keys are not expressible in the DSL.** The hooks stop a
request naming a list in another workspace; they cannot stop a migration, a
repair script or an endpoint written next year. `cmd/migrate` adds
`tasks (workspace_id, list_id) → lists (workspace_id, id)` as a hand-written
`migrate.Change`, which makes the wrong reference unrepresentable rather than
merely unreachable. The unique index it needs *is* expressible, so half of it
lives in the schema with a comment pointing at the other half.

The same file adds two triggers, for the same reason: `updated_at` is otherwise
only ever set by its column default, and `tasks.completed_at` has to be
reconciled with `tasks.status` by something that can see the new row — which a
`BEFORE` trigger can and a `BeforeUpdate` hook cannot, since a hook is handed the
statement rather than the result.

## Authentication

Self-issued HS256, written out rather than imported, so the path from
`Authorization:` header to `WHERE` clause is readable without leaving the module.
[`auth/jwt.go`](auth/jwt.go) is about a hundred lines of standard library.

It is not a general-purpose JWT library. A service verifying tokens it did not
mint — Auth0, Keycloak, Cognito, Clerk — needs JWKS and RS256, and should use
`github.com/golang-jwt/jwt` rather than extending this. What is *not* simplified
is the verify path, because that is where a JWT implementation becomes an
authentication bypass:

- the algorithm is pinned **before** the signature is used, so `alg: none` and
  RS256-confusion are refused without either being attempted;
- the comparison is `hmac.Equal`, not `bytes.Equal`;
- expiry is required, not merely checked when present — a missing `exp`
  unmarshals to zero, and treating zero as "no expiry wanted" is an immortal
  token.

`auth/jwt_test.go` builds each of those forgeries and asserts it is refused.

Passwords are PBKDF2-HMAC-SHA256 at 600,000 iterations, with a per-password salt
and a constant-time comparison. PBKDF2 only because `crypto/pbkdf2` is in the
standard library as of Go 1.24; argon2id is the better choice and lives in
`golang.org/x/crypto`.

Two things a real deployment needs and this does not have: token revocation (the
TTL is the only bound on a logout or a removed membership taking effect) and a
refresh endpoint. Both are noted where they bite.

## Regenerating

```bash
go generate ./...              # models, typed columns, REST bodies, manifest
go run ./cmd/migrate -force    # migrations, from the schema
```

`mise run generate-check` fails if the committed output has drifted from the
schema. The migrations are *not* checked that way: `migrate.Write` refuses to
overwrite, because a migration already applied somewhere must not change under
the runner's feet. `-force` is the development escape, and it deletes only the
files it is about to write.

## Testing

```bash
mise run test-demo             # needs Docker
```

Against a real Postgres in a container, one database per test. There is no
skip-when-Docker-is-absent path, for the reason `pgtest/doc.go` gives: a suite
that passes silently when it cannot reach a database reports coverage it does not
have. Several claims here can only be checked this way — that the composite
foreign keys reject a cross-workspace reference, that the `completed_at` trigger
fires, that a failed comment leaves the counter untouched.
