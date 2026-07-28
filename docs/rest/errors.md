# Rejections

A column that does not declare a capability cannot be reached through it, and
the rejection is data rather than prose
([ADR-0011](../adr/0011-actionable-errors.md)):

```json
{
  "title": "Bad Request", "status": 400,
  "detail": "one or more query parameters were rejected",
  "errors": [{
    "message": "column is not sortable",
    "location": "query.sort", "value": "body",
    "allowed": ["title", "status", "view_count", "published_at", "created_at"]
  }]
}
```

Every problem in a request is reported at once, not one per round trip — a
malformed request takes one round trip to fix rather than one per mistake. The
caller most likely to read this is a program assembling requests against a
schema it only partly knows, and "column is not sortable" is a dead end where
the same message plus the sortable columns is a fix.

The full catalogue of messages is in the
[rejection reference](https://jryannel.github.io/sqlb/reference/rejections/).

## Reading it in Go

Reach the structured form with `filter.AsErrors`, which unwraps as it goes.
Prefer it to a type assertion, which panics the moment a middleware wraps the
error:

```go
if errs, ok := filter.AsErrors(err); ok {
    errs.WriteHTTP(w)
    return
}
```

## A hidden column appears nowhere

Not as a parameter, not in the response schema, and **not in that `allowed`
list**. It cannot be recovered by probing.

That is the difference between "not permitted" and "not present", and it is why
`Hidden` plus `Filterable` is a schema validation error rather than a
combination you can write: a filterable secret can be recovered a character at a
time by an attacker who is patient about 200s and 404s.

## What each status means here

| Status | Cause |
|---|---|
| 400 | The query string could not be understood, or named something that has not opted in. Carries `errors[]` with allow-lists |
| 404 | No row matched, after hooks applied their predicates. A row confined away by a tenant scope is indistinguishable from one that does not exist, which is the intent |
| 409 | A constraint the application chose to state — a unique index, a domain rule a hook turned into a refusal |
| 422 | The body parsed but is not acceptable: cross-field validation from `Row()`, or a hook that refused |
| 500 | Anything else. Its text is a wrapped Postgres error with table and constraint names in it, so it goes to the log rather than to the caller |

The line between 409/422 and 500 is a decision worth making explicitly:
everything the application recognises is a rule it chose to state, so its text is
the answer; everything else is a 500 whose text the caller does not get.

## It survives to the last consumer

The allow-list reaching a JSON body is only half the guarantee. The generated
[TypeScript client](../typescript/README.md) types that body rather than
flattening it to a message, so a UI can offer the alternatives:

```ts
import { allowedFor, isProblem } from './api/client.gen';

if (isProblem(body)) {
  const sortable = allowedFor(body, 'query.sort');  // ["title", "view_count", ...]
}
```

And the [CLI](../cli/README.md) prints it, keeping the list intact:

```
$ taskctl tasks list --sort -nonexistent
Error: the request could not be understood (HTTP 400)
  query.sort: column is not sortable
    allowed: title, status, priority, due_at, completed_at, position, comment_count
```

For the CLI there is a stronger version still: a column that never declared
`.Filterable()` has **no flag at all**, so the request the server would reject
has no spelling. The rejection is the fallback, not the mechanism.

## Next

- [Mounting resources](README.md)
- [Capabilities](../concepts/capabilities.md) — why the refusal is shaped this way
- [ADR-0011](../adr/0011-actionable-errors.md) — the decision record
