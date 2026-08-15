# TypeScript client

The REST layer's filter grammar is precise on the server: a column that never
declared `.Filterable()` cannot be filtered on, and the rejection says what
would have been accepted. None of that reaches a browser by itself. A
hand-written client spells `?status=eq.published` into `URLSearchParams` and
takes half a dozen bare `string` parameters, and a typo compiles.

`codegen` emits a TypeScript client that closes that gap, from the same schema
declaration the Go models come from.

## Turning it on

Set `TSDir` on the generator you already have:

```go
codegen.Must(codegen.Generate(codegen.Options{
    Registry: schema.DefaultRegistry(),
    Dir:      "blog",
    Package:  "blog",

    // Relative to Dir. Three files land here; nothing is emitted without it.
    TSDir: "web/src/api",
}))
```

Three files, because the layers are usable separately:

| | |
|---|---|
| `runtime.gen.ts` | `Page`, `Collection`, `Problem`, `Transport` and the filter encoder — the part that depends on no schema. **Imports nothing.** |
| `client.gen.ts` | Row types, request bodies, the typed parameter vocabulary, one function per exposed operation, and the cache keys — plus a write-result type per table that has a `Needs` column, since a create or update cannot answer with the same shape a read can. Imports the runtime, and re-exports it. |
| `queries.gen.ts` | TanStack Query `queryOptions`, `infiniteQueryOptions` and `mutationOptions`. Takes `@tanstack/react-query` as a peer dependency. Set `TSQueriesFile: "-"` to skip it. |

The runtime is a file of its own so that an application with more than one
generated module has one `Page` and wires one `Transport` rather than N
([#110](https://github.com/jryannel/sqlb/issues/110)). Point two modules at one
`TSDir` and they share it: nothing in it is schema-specific, so the second
writer produces the same bytes and `check` stays meaningful for both.

A project with one module need not notice. `client.gen.ts` re-exports
everything the runtime holds, so `import type { Page } from './client.gen'`
keeps compiling exactly as it did.

The client is emitted into the repository that consumes it, the way
`models_gen.go` is. There is no npm package to install and therefore no way for
the client to be a version behind the server it talks to.
`codegen.Check` covers all three, so the usual staleness gate catches a schema
change that was never regenerated.

## What the types know

Everything a column declared, and nothing it did not:

```ts
const page = await listPosts(request, {
  where: {
    status: { in: ['draft', 'published'] },  // the enum's values
    title: { contains: search },             // pattern operators: it is text
    published_at: { notnull: true },         // a null test: it is nullable
    view_count: { gte: 100 },
    labels: { has: 'urgent' },               // containment: it is an array
  },
  sort: ['-published_at', 'title'],
  select: ['title', 'status'],
  expand: ['author'],
  per_page: 50,
});
```

- **`where` admits filterable columns only**, and the operator set is narrowed
  by column type. `contains` on a number does not compile; neither does
  `isnull` on a non-nullable column, nor a value outside an enum.
- **An array column takes `ArrayCond`**: `has` for one element, `hasany` and
  `hasall` for a list, their negations `nhas`, `nhasany` and `nhasall`, and a
  bare array for whole-array equality. It has no
  `contains` — that is the text substring operator, and one name meaning two
  things depending on the column is precisely the ambiguity this client exists
  to remove. It has no ordering operators either. The negations are `NOT (…)`
  rather than complements, so a null column matches neither `has` nor `nhas`.
- **`sort` is a union** of the sortable columns and their `-` forms. An array
  column is never in it.
- **`select` narrows the response type.** `page.items[0].title` is available
  after the call above; `page.items[0].body` is not. The primary key is always
  present, because the server adds it back to any projection that dropped it.
- **`expand` widens it.** A forward relation resolves to the row type; a reverse
  one to `Collection<T>` — `{items, has_more}` — so a capped expansion cannot
  be mistaken for a complete list.
- **Hidden columns have no spelling anywhere.** Not in the row type, not in
  `select`, not in `where`.
- **A column carrying `Needs(...)` is missing from what `create`/`update`
  return.** Its expression depends on who is asking, and a write has no
  per-request bind to resolve that with, so the response leaves the key out
  ([ADR-0041](../architecture.md#computed-fields)). `createPost` and `updatePost`
  are typed as returning `PostWriteResult` rather than `Post` whenever the
  table has one of these — a distinct generated type, so a read and a write
  cannot silently drift back into sharing the wrong one.

This is [the typed column facade](../queries/typed-columns.md) carried across the wire, and it is
why the client is generated from the schema rather than from the OpenAPI
document: the document can only say `array<string>` about a filter parameter,
with the operators in prose.

## The transport is yours

The generated functions take a request function as their first argument rather
than constructing one. Base URL, auth header, refresh, retry and what a 401 does
are not derivable from a schema, and are the parts of a real client that matter
most:

```ts
import { type ApiRequest, type Transport } from './api/client.gen';

export const request: Transport = async <T>({ method, path, query, body, signal }: ApiRequest): Promise<T> => {
  const res = await fetch(`${BASE}${path}${query ? `?${query}` : ''}`, {
    method,
    headers: {
      ...(body === undefined ? {} : { 'content-type': 'application/json' }),
      ...(token() === null ? {} : { authorization: `Bearer ${token()}` }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  if (!res.ok) throw await res.json();
  return res.status === 204 ? (undefined as T) : ((await res.json()) as T);
};
```

This is the same seam `rest` takes by mounting onto a `huma.API` you built. It
also means the generated functions compose with hand-written ones: a login
endpoint is not a table, and no schema generator will produce it.

## Rejections keep their allow-list

A 400 from the filter grammar carries what would have been accepted. The client
types that body rather than flattening it to a message, so a UI can offer the
alternatives:

```ts
import { allowedFor, isProblem } from './api/client.gen';

if (isProblem(body)) {
  const sortable = allowedFor(body, 'query.sort');  // ["title", "view_count", ...]
}
```

## Paging

`next_cursor` is on every list response with a page after it, which is exactly
the shape `infiniteQueryOptions` wants:

```ts
const feed = postQueries(request).infinite({ sort: '-published_at', per_page: 50 });
```

`page` and `cursor` are absent from that factory's parameters: they are two
answers to where a page starts, and the factory owns the answer. Paging by hand
is the same loop with `cursor` threaded through `next_cursor`, and it costs the
same at any depth.

## Writes

One `mutationOptions` object per write, carrying `mutationFn` and nothing else:

```ts
const tasks = taskMutations(request);

useMutation(tasks.create);   // mutate({ list_id, title, description })
useMutation(tasks.update);   // mutate({ id, body })
useMutation(tasks.delete);   // mutate(id)
useMutation(tasks.complete); // a declared verb, mutate({ id, body })
```

The variables are the body types from `client.gen.ts`, so a read-only column has
no spelling here either — an argument that would earn a 400 does not compile.

There is no generated `onSuccess`. What a write invalidates depends on which
views the application keeps, and a computed view is not a table, so its key
cannot be derived at all. Policy is spread in rather than edited out:

```ts
useMutation({
  ...taskMutations(request).update,
  onSuccess: (task) => {
    queryClient.invalidateQueries({ queryKey: taskKeys.lists() });
    queryClient.invalidateQueries({ queryKey: taskKeys.detail(task.id) });
  },
});
```

`keysByTable` below is the mechanical half of that; the choice of what to
invalidate stays yours.

## Cache keys

One factory per resource, plus a table-keyed index:

```ts
taskKeys.lists();                 // ['tasks', 'list']
taskKeys.detail(id);              // ['tasks', 'detail', id, {}]
keysByTable['tasks'].lists();     // the same list, reached from an event payload
```

The index exists so that a change event — a table plus a row key — maps onto
cache keys mechanically. Two hand-maintained invalidation lists drift; the bug
that motivated this was `['draft', id]` against `['drafts', id]` in a client
where mutations and an event stream each kept their own list.

## What is not generated

Hooks, write policy, optimistic updates, a client object, an npm package. Hooks
bake in a framework and get copied out and edited; an options object is spread
and overridden instead:

```ts
{ ...postQueries(request).list({ sort: 'title' }), staleTime: 30_000 }
```

`example/tasks/web` is a worked one, including
[the refusals](../../example/tasks/web/src/refusals.ts) — the requests that must
*not* compile, asserted with `@ts-expect-error` so that a generator which
widened a type fails the build.
[ADR-0028](../architecture.md#typescript-client) records the reasoning, including
what would make the whole approach wrong.

## Next

- [Mounting resources](../rest/README.md) — the server side of the same grammar
- [Dart client](../dart/README.md) — the same design where the language cannot narrow
- [Go CLI](../cli/README.md) — the same argument for a consumer with no compile step
- [Capabilities](../schema/capabilities.md) — the declarations these types come from
