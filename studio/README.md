# studio — an uncurated browser over `sqlb.json`

A generic data/schema/action browser: point it at a manifest and a running
application's REST API, and it renders a grid, not a curated admin. See the
package doc (`go doc ./studio`) for the argument and
[docs/adr/0053](../docs/adr/0053-the-manifest-describes-what-cannot-be-guessed.md)
for the decision this module exists to test.

It is a module of its own, like `pgtest` and `example/tasks`, so its
dependencies stay off the engine's `go.mod` and its release cadence stays off
the engine's — `mise run deps-check` still reports **standard library only**
for the root module.

## Running it

```bash
go build -o sqlb-studio ./studio/cmd/sqlb-studio
./sqlb-studio -manifest ./sqlb.json -api http://localhost:8080
```

Then <http://localhost:4000>. Without `-api`, the schema pages still work —
tables, columns, capabilities, declared actions — but every data page
redirects to a login screen there is no API to serve it against.

Sign in with a bearer token from the application itself (however it issues
one — studio has no opinion). It is attached as `Authorization: Bearer …` on
every request studio makes on your behalf, so you see exactly the rows and
actions that token can already reach — the same row-scoping hooks apply here
as they do to `?expand=` on the API directly.

```bash
go build -o sqlb-studio ./studio/cmd/sqlb-studio && \
./sqlb-studio -manifest example/tasks/sqlb.json -api http://localhost:8080
```

against a running [`example/tasks`](../example/tasks) server is the fastest
way to see it against a real schema.

## What to read, and in what order

| | |
|---|---|
| [`doc.go`](doc.go) | The argument: why an uncurated grid needed nothing the manifest was already declining to carry for a curated one. |
| [`manifest.go`](manifest.go) | `LoadManifest` — the only place this module reads a file. Everything else is an HTTP client and a renderer. |
| [`server.go`](server.go) | Routes, and the shape every handler follows: fetch through `client.go`, render through `form.go`'s field builders. |
| [`client.go`](client.go) | The REST client. Decodes into `map[string]any` — there is no generated type to decode into, which is the whole point. |
| [`form.go`](form.go) | Where a column or an action's declared `Body` becomes an HTML field, and a submitted field becomes a typed JSON value. The narrowest part on purpose: a checkbox for `bool`, a `<select>` for a declared `Enum`, text with a hint for everything else. |
| [`server_test.go`](server_test.go) | An `httptest` fake standing in for a generated REST API — login, the grid, edit, create, and both action shapes, each against real request/response bodies rather than the templates in isolation. |

## What it does not do

No service credential and no cross-tenant view — every request goes through
the API as the signed-in operator. No logs or traces: that's "instrument,
don't carry," and the seam already exists
([`example_trace_test.go`](../example_trace_test.go) wraps `sqlb.Executor`
for OpenTelemetry, Uptrace, or a Grafana dashboard, none of which this module
needs to know about). No optimistic concurrency — a second operator's
concurrent edit overwrites the first, same as calling `PATCH` by hand would.
