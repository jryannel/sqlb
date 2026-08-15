# A cross-tenant admin surface

A platform-admin view — every workspace's rows, not just the caller's — is two
independent decisions, not one. Getting only one of them right is worse than
getting neither: a release with no route guard lets any authenticated user
reach every tenant by knowing the path, and a route guard with no release
serves a 200 with an empty page, since the ordinary hooks are still filtering
to a workspace an admin token names nothing meaningful for.

[`example/tasks/app/admin.go`](../../example/tasks/app/admin.go) is both
halves, worked: six `/admin/*` resources over a six-table multi-tenant task
manager, tested against real Postgres.

## The two halves

**Row visibility is a released scope.** Name the hook that confines the
ordinary surface, and mount a second resource that releases it:

```go
// The rule, named — which is what makes it releasable at all.
sqlb.On[Task](reg).Scope("tenant").BeforeQuery(scopeToWorkspace)

// The admin mount, over the same generated model, released from it.
err := rest.Resource[tasks.Task, rest.None[tasks.Task], tasks.TaskPatch](
    api, hooked, rest.Options{
        Path:     "/admin/tasks",
        Name:     "admin-task",
        Tag:      "admin",
        Ops:      rest.OpRead | rest.OpUpdate | rest.OpList,
        Unscoped: []string{"tenant"},
    })
```

An ordinary `BeforeQuery` has no name and cannot be released — that asymmetry
is deliberate, so the decision to make a rule negotiable sits with whoever
wrote it. See [Capabilities: the other half,
rows](../schema/capabilities.md#the-other-half-rows) and
[ADR-0054](../adr/0054-a-named-scope-is-releasable-at-the-mount.md).

**Route access is a guard the release does not provide.** Releasing a scope
only changes what rows a query can see, never who may ask, so the mount needs
a second, independent check in front of it:

```go
// A route guard, ahead of the ordinary auth middleware:
func RequireAdmin(prefix string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if !strings.HasPrefix(r.URL.Path, prefix) {
                next.ServeHTTP(w, r)
                return
            }
            claims, err := Require(r.Context())
            if err != nil || !claims.PlatformAdmin {
                forbidden(w, "this route needs a platform-admin token")
                return
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

```go
router.Use(auth.Middleware, auth.RequireAdmin("/admin/"))
```

There is no `rest.Options` field for this half, and there should not be one —
who counts as a platform admin, and how that claim is minted, is an
application decision the same way a router or a migration runner is. `rest`
gives you the mount; the guard is middleware on your router, ahead of it, the
same as any other auth check.

## Wiring it together

```go
router.Use(
    middleware.RequestID,
    middleware.Recoverer,
    auth.Middleware,           // rejects an unauthenticated request
    auth.RequireAdmin("/admin/"),   // then: admin routes need PlatformAdmin
)

api := humachi.New(router, huma.DefaultConfig("Tasks", "1.0.0"))
if err := tasks.Register(api, hooked); err != nil {   // the ordinary, tenant-scoped surface
    return err
}
if err := registerAdminRoutes(api, hooked); err != nil {   // the released, admin-guarded one
    return err
}
```

Both mounts share the same hooked `*sqlb.DB` — `hooked` — because releasing a
scope is a property of the *request*, not of a second database handle: the
resource passes `Unscoped: []string{"tenant"}` in its own `rest.Options`, and
every other hook still runs.

## What this buys, and what it costs

**No create anywhere in it, on purpose.** `BeforeCreate` is not releasable
([ADR-0054](../adr/0054-a-named-scope-is-releasable-at-the-mount.md) —
"there is nothing for a reader to be released from"): it stamps the tenant
column from the caller's own claims, and a platform-admin token names no
workspace a new row should belong to. An admin creating a row in a specific
tenant still does it through that tenant's own token.

**It is hand-mounted, not schema-declared.** `Expose` stays singular per table
([ADR-0050](../adr/0050-reachability-is-a-property-of-the-mount.md)), so this
file is not generated: it carries no entry in `sqlb.json`, no generated
TypeScript or Dart client, and is not on the drift gate. A schema edit here
needs `admin.go` updated by hand, the same as any other hand-written endpoint.

**Minting the token is out of band.**
[`cmd/mint-admin`](../../example/tasks/cmd/mint-admin/main.go) is the worked
example's way of producing a `PlatformAdmin` claim — a CLI a human runs, not
an endpoint the API serves, since there is no tenant context to authenticate
an admin request against and building one is a separate decision the example
deliberately leaves alone.

## Next

- [Capabilities: one table, two surfaces](../schema/capabilities.md#one-table-two-surfaces) — the row half, in full
- [Hooks: when one surface is the exception](../queries/hooks.md#when-one-surface-is-the-exception) — naming and releasing a scope
- [`example/tasks/app/admin.go`](../../example/tasks/app/admin.go) — the worked example
- [`example/tasks/app/admin_test.go`](../../example/tasks/app/admin_test.go) — what it asserts
- [ADR-0050](../adr/0050-reachability-is-a-property-of-the-mount.md) — why this is hand-mounted rather than declared
- [ADR-0054](../adr/0054-a-named-scope-is-releasable-at-the-mount.md) — the release mechanism
