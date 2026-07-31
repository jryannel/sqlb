# Actions

CRUD covers what a row *is*. It does not cover what happens to it. Completing a
task, archiving a project, publishing a post — these are transitions with rules,
and the way to say one through a PATCH is to send the columns the transition
would have written and hope the server agrees.

A declared action gives the transition a route, and generates everything around
it except the transition itself ([ADR-0043](../adr/0043-declared-actions.md)):

```go
Task.Action(schema.Action{
    Name:   "complete",
    Body:   schema.Body(schema.Text("note").Nullable()),
    Writes: []string{"status", "completed_at"},
})
```

That serves `POST /tasks/{id}/complete`, and asks the application for one func.

## What is generated, and what is not

**Generated:** the route, the OpenAPI operation, the request body type, the id
parse, the scoped fetch, the 404, the transaction, the row lock, the write of
the declared columns, the response — and the same verb in the TypeScript client,
the Dart client and the CLI.

**Not generated:** the transition. That is a plain Go func, the seam
`BeforeCreate` already uses:

```go
func completeTask(ctx context.Context, task *tasks.Task, in tasks.CompleteTaskInput) error {
    if task.Status == tasks.TaskStatusDone {
        return &rest.Problem{
            Title:  http.StatusText(http.StatusConflict),
            Status: http.StatusConflict,
            Detail: "the task is already done",
        }
    }
    now := time.Now().UTC()
    task.Status = tasks.TaskStatusDone
    task.CompletedAt = &now
    return nil
}
```

The split is deliberate and it is the whole design. Domain logic is where a
DSL's expressiveness runs out first, and [vision.md](../vision.md) names the
consequence: generated handlers that get copied out and edited by hand mean the
seams were in the wrong place. So nothing here tries to *express* the
transition. `Writes` says which columns it may leave changed; the rest is Go.

## Binding the func

When any table declares an action, `Register` grows a parameter:

```go
if err := tasks.Register(api, db, tasks.Actions{
    CompleteTask: completeTask,
}); err != nil {
    return err
}
```

`Actions` has one field per declared verb, with the exact signature the envelope
will call. That is the compiler's half: adding an action to the schema fails the
next build at this call site rather than serving a route nobody wired. The other
half is at startup — `Actions{}` compiles, so a nil field is refused when the
resource mounts, naming the field to go and set
([ADR-0030](../adr/0030-declared-scope-is-required.md)'s shape).

## The write set is enforced

The envelope writes exactly the columns `Writes` names, taken off the row the
func mutated. A column the func changed and the declaration did not name stays
unwritten.

That is what makes `Writes` a statement about the route rather than a comment on
it: `sqlb impact` reports it, the OpenAPI document carries it, and `taskctl tasks
complete --help` prints it. A verb that has to touch anything else has the
transaction and can issue the statement:

```go
tx, ok := sqlb.TxFrom(ctx)
if !ok {
    return rest.ErrNoTransaction
}
_, err := sqlb.InsertRows(&tasks.Comment{TaskID: task.ID, Body: *in.Note}).One(ctx, tx)
```

The comment and the completion commit together or neither does.

`Writes` also decides the lock. Every one of these is a read-modify-write across
a round trip, so a declared write set makes the fetch `SELECT … FOR UPDATE` —
without it, two concurrent completions read the same row and the second
overwrites the first. Nobody has to remember it per route.

## Scoping comes with the fetch

The envelope's fetch runs the model's `BeforeQuery` hooks, so an action on a
`Scoped` model is confined by the same registration its list and read endpoints
are — and refuses to mount without one. Hand-written verb handlers are precisely
where the tenant predicate is otherwise remembered by hand, on the majority of an
application's routes.

A request for another tenant's row gets a **404**, not a 403: the row was never
in the query.

## Collection actions

A path with no `{id}` addresses the collection. There is no row to fetch, so the
func receives only the body and the response is a 204:

```go
Project.Action(schema.Action{
    Name: "purge-archived",
    Path: "/purge-archived",
})
```

**Note what is absent along with the fetch.** No `BeforeQuery` runs, so a
declared scope obliges nothing here, and confining the statements the func issues
is the func's own job — the position `sqlb.Query[T]()` in application code is
already in.

## The body

Declared in the field vocabulary, not reflected from an application type:

```go
Body: schema.Body(
    schema.Text("note").Nullable(),
    schema.Timestamp("completed_at"),
),
```

The reason is that the emitters read the declaration. A body sqlb cannot see
produces a TypeScript function typed `unknown`, which is most of what an action
was supposed to remove. Only what describes a *value* applies — name, type,
nullability, enum values, default, comment. A property claiming `Filterable` or
`Hidden` is a schema validation error rather than something quietly ignored.

Optionality follows the create body's rule: a nullable or defaulted property may
be omitted, everything else is required.

An action that declares no body still gets an input type on the Go side, empty,
so that declaring the first property later does not change the signature of the
func you already wrote. The operation reads no request body until there is one.

## Errors

| The func returns | The client sees |
|---|---|
| `nil` | 200 with the row, or 204 for a collection action |
| `*rest.Problem` | that problem's own status — this is how "cannot complete an archived task" is a 409 |
| anything else | 500, with the error logged and the transaction rolled back |

A failing func rolls back everything, including whatever it wrote through the
transaction itself.

## What is deliberately not declarable

Preconditions, guards, and state machines. The moment the schema can say "refuse
if archived", it is expressing the transition, and the failure mode above is
live. Refusing is what the func is for, and a one-line refusal in Go is not the
thing anyone is asking to be freed from.
