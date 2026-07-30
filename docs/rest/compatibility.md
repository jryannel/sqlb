# API compatibility

`sqlb migrate` asks whether a schema edit is safe for the *database*. `sqlb
impact` asks whether it is safe for the *deployed REST client* — and the two
answers are different, and often inverted.

The REST contract a schema generates — the response fields, the filter and sort
parameters, the `?expand` relations, the create and patch bodies, the operation
set — is a pure function of the model's capabilities
([ADR-0007](../adr/0007-generated-rest-handlers.md)). So sqlb is the one thing
that knows how a schema edit changes that contract, and `sqlb impact` reports it
([ADR-0039](../adr/0039-a-schema-edit-is-an-api-edit.md)).

## Why this is not the migration check

A migration diff reads the columns and types that produce DDL. It cannot see the
breaks that matter most to a client, because the sharpest ones produce no DDL at
all:

| Change | Migration | REST contract |
|---|---|---|
| Rename `title` → `headline` (with `RenamedFrom`) | a clean, reversible `RENAME` | **breaks** every client reading or writing `title` |
| Stop exposing a column, or drop `OpRead` | no DDL at all | **breaks** clients, invisibly to any migration |
| Add a nullable filterable column | safe | additive — a new optional parameter and field |
| `NULL` → `NOT NULL` on an exposed column | a lock hazard | readers unaffected; the **create body now requires it** |
| Drop a column | destructive, commented out | breaking — the field and its filter vanish |

The cleanest migration sqlb can emit — a declared rename — is a hard wire break,
because the wire spelling of a column *is* the column's name
([ADR-0036](../adr/0036-the-wire-is-the-column-name.md)). That is the case that
makes `impact` a check of its own rather than something the migration gate could
have told you.

## The three commands

```bash
sqlb impact -write ./schema     # record the current contract as the baseline
sqlb impact ./schema            # report how the contract has moved; always exits 0
sqlb impact -error ./schema     # the same report, but exit non-zero on a breaking change
```

`impact` diffs the current schema against a **committed baseline** — the file
`restcontract.json` beside the generated code, or wherever `Project.ContractFile`
points. That file is the concrete answer to "backward compatible *relative to
what?*", and it belongs in the repository like the migration history does. Record
it once with `-write`, review it, commit it. Re-record it deliberately whenever
you decide a contract change is intended.

A run against an unchanged schema says so and writes nothing:

```
sqlb: the REST contract is unchanged
```

A run against a changed one prints one line per change, most breaking first:

```
breaking /posts response.headline  renamed from "title"; the migration is a clean RENAME but the wire name changed
breaking /posts filter.headline    renamed from "title"; ?title=… now 400s
breaking /posts filter.status      filter removed; a request using it now 400s
sqlb: 3 contract change(s), 3 breaking
```

## What the levels mean

- **breaking** — a request that worked now fails, or a response field a client
  relied on is gone or changed shape.
- **additive** — a new capability. Nothing a client sends or reads today changes
  meaning: a new optional filter, a new field, a newly exposed operation.
- **neutral** — a change with no client effect, such as a response field going
  from nullable to always-present.
- **unknown** — a real change whose effect depends on how a specific client
  generated its types. A widened integer is the example: a reader with a narrower
  generated type can overflow on a value the wider column now permits. `impact`
  surfaces it rather than claiming it safe, and `-error` counts it as breaking.

A single edit can be reported on two sides at once. A column going `NOT NULL` is
`neutral` in the response and `breaking` in the create body, and both lines are
printed — the reader-safe half never hides the writer-breaking half.

## Gating it in CI

Default `impact` states the delta and exits 0, because whether a break matters
depends on facts the schema does not hold — whether you have deployed clients,
and whether you version your API. Turn it into a wall where you know the answer:

```bash
sqlb impact -error ./schema
```

This project gates its own example that way:

```bash
mise run impact-check
```

which fails if the blog example's committed contract has a breaking change that
was not re-recorded. Add the same line to your CI beside `sqlb check`.

## What is in scope

`impact` speaks about the contract sqlb *generates*, and only that. Once you
write a custom handler, reshape a response in a hook, version your own `/v1` and
`/v2`, or put a gateway in front, the true client contract is no longer a
function of the schema — the same boundary `migrate` draws for the DDL it emits
against a production database it never reads.
