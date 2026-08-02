# Schema practices, and which ones sqlb enforces

sqlb is opinionated about schema design. Some of those opinions are enforced by
the DSL — you cannot express the alternative — and the rest are recommendations
the tooling reports on but permits.

This page separates the two, because conflating them is how a missing feature
gets rewritten as a principle. Every practice below is stated with the reason it
would survive if sqlb vanished tomorrow, and the last section lists the places
where sqlb refuses something for no better reason than that it is unbuilt.

## Where the evidence comes from

Claims about how often something occurs come from `sqlb-survey` run over two
production schemas: **valiro-go** (68 tables) and **mind-vm/studio-apps** (ten
independent app deployments, 233 tables). 301 tables total. Where a practice is
supported by a count, that is where the count is from. Where it is not, it says
so — a practice can be right without being measured.

## Enforced

Each of these is unrepresentable in the DSL. The design is recorded in an ADR
with the argument and the cost.

### A module owns its tables, and its prefix is mechanical

[ADR-0015](adr/0015-module-isolation.md). `schema.NewModule("billing")` returns a
registry that prefixes every table it holds, so declarations use the local name
(`Table("invoices")` → `billing_invoices`) and the prefix cannot be forgotten.

**Why it stands alone:** in one database, unprefixed module tables collide. Not
theoretically — studio-apps documents the rule (*"new modules MUST prefix every
table with `<module>_` … `rag`'s tables were renamed after colliding with an
app's `documents` table"*) and enforces it with a `module-check` target, and it
still drifted: of its core modules, 6 tables conform, 8 violate the rule, 16 are
permanently grandfathered. The 6 non-grandfathered violations — `agentdeploy`'s
`deployment*` tables and `agentloop`'s `agent_sessions`/`agent_steps` — are
exactly the tables that collided when the survey built one database per app.

A rule enforced by review drifts. A prefix applied by the registry cannot.

**Prefixes, not Postgres schemas.** ADR-0015 takes `billing_invoices` over
`billing.invoices` deliberately: schemas are a deployment model, not a rendering
strategy, and adopting them means `search_path` management, `CREATE SCHEMA`
ordered ahead of each module's first migration, and per-schema migration tables.
Naming is the easy quarter of that problem.

### An enum is text with a CHECK, not a Postgres ENUM

[ADR-0017](adr/0017-enums-as-text-and-check.md).

**Why it stands alone:** a native enum cannot drop a value — there is no
`ALTER TYPE … DROP VALUE`, only a replacement type, a rewrite of every column
using it, and a drop. `ALTER TYPE … ADD VALUE` cannot run in the transaction
that reads the new value, which drags every unrelated change in the migration
out of its transaction too. And the type is schema-level, so two modules
declaring `status` collide in a namespace neither owns.

The cost is real and stated: storage compactness, a defined sort order, and
type-level rejection at every call site.

### A row is addressed by one column

[ADR-0034](adr/0034-one-column-addresses-a-row.md). A composite key becomes a
`UniqueIndex`; the table carries a surrogate for identity and the unique index
keeps the real key real.

**Why it stands alone:** a row has one spelling — in the URL, the cursor
payload, the `?expand` aggregation, the generated cache key — and each of those
is a wire format that freezes on its first response. `/tasks/{a},{b}` invents an
encoding and then needs an escape rule for a key containing a comma.

**This one has a live revisit trigger, and this corpus pulled it.** ADR-0034
narrows its own claim: the refusal only needs to reach tables that are exposed,
cursor-paged or expanded, and it currently sits in the registry, which is wider.
Its stated trigger is *"a real schema wanting a composite key on a table that is
not exposed, expandable or cursor-paged"*. That had one instance
(`llmcatalog`). studio-apps supplies more: composite primary keys appear in 4 of
10 apps, and `agentdeploy_deployment_env` / `deployment_project_env` /
`deployment_active_version` are configuration tables keyed `(deployment_id, key)`
that no REST resource mounts.

So the evidence says **move the refusal, not remove it** — into
`rest.Resource`'s mount check and `keysetTerms`, as the ADR already proposes.
That is the honest reading, and it is a smaller change than "support composite
primary keys."

### The wire spells a column the way the schema does

[ADR-0036](adr/0036-the-wire-is-the-column-name.md). One spelling, from the
column to the JSON key.

**Why it stands alone:** the alternatives relocate drift rather than removing it.
Renaming columns to camelCase means quoted identifiers in Postgres forever;
putting the mapping in the transport moves it into hand-written code that sits
outside every gate.

This is the most expensive opinion sqlb holds for an adopter with a camelCase
front end — 236 routes at once, in the one codebase that counted — and it is
tracked as [#116](https://github.com/jryannel/sqlb/issues/116), which argues the
single spelling could be *derived* rather than literal. Unresolved.

### A vector column declares its dimension, and similarity search is its own operation

[ADR-0026](adr/0026-vectors-declare-their-index.md). The dimension is part of the
type and is an ordinary Go expression:

```go
schema.Vector("embedding", ragcfg.Dim).Searchable()
```

An index is a **second, optional** decision, taken when exact search stops being
fast enough — the unindexed declaration is complete on its own.

**Why it stands alone:** the dimension is a property of the embedder, so binding
it to a Go constant means the schema and the model that fills it cannot disagree
silently. studio-apps writes its migration the other way — a
`vector(%%EMBEDDING_DIM%%)` placeholder substituted at deploy time from
`RAG_EMBEDDING_DIM` — and notes in its own config that once migrated, changing
the variable does not alter the column. That is the drift this closes.

Confirmed working end to end: `vector(1536)` columns round-trip intact through
introspect and Diff, with no skip note anywhere across 233 tables.

## Recommended, not enforced

sqlb imports these happily. They are worth doing anyway, and the round trip will
tell you about them.

### `text`, not `varchar(n)`, when a CHECK already bounds the value

A length cap beside a CHECK constraining the value to a fixed set is redundant,
and the two disagree the moment the set changes. In valiro this was four status
columns; converting them took the round-trip residual to **0**.

Postgres stores `text` and `varchar` identically — there is no performance
argument for the cap, only a validation one, and the CHECK is doing that job.

### `gen_random_uuid()`, not `uuid_generate_v4()`

`gen_random_uuid()` is built in from Postgres 13. `uuid_generate_v4()` requires
the `uuid-ossp` extension, which then has to exist in every database anyone
creates — including scratch and test databases, where its absence fails once per
table naming a missing *function* rather than once naming the missing extension
([#115](https://github.com/jryannel/sqlb/issues/115)).

In valiro this was 30 columns and it dropped a dependency.

### Prefer identity over serial, and expect neither to import yet

`GENERATED … AS IDENTITY` is the SQL-standard spelling; `serial` is a legacy
pseudo-type that expands into a column, a sequence, and a default, with the
sequence as a separate object you now own.

Be aware this is a recommendation about *your* schema and not a claim about
sqlb: sqlb imports neither. See the gaps below.

## Not opinions — gaps

These are refusals with no design argument behind them. They are listed here so
this page cannot be read as a justification for them.

| gap | seen in |
|---|---|
| [#108](https://github.com/jryannel/sqlb/issues/108) composite `UNIQUE` constraint has no table-level declaration | 8 of 10 studio-apps apps, 5 valiro tables |
| [#114](https://github.com/jryannel/sqlb/issues/114) `smallint` | 2 apps, 6 valiro columns |
| [#120](https://github.com/jryannel/sqlb/issues/120) `real` | 2 columns |
| [#121](https://github.com/jryannel/sqlb/issues/121) `EXCLUDE` constraints | 1 app — and no near-miss; dropping it loses an invariant |
| [#115](https://github.com/jryannel/sqlb/issues/115) `Diff` renders no `CREATE EXTENSION` | every bootstrap |
| identity and serial columns | both refused; [#119](https://github.com/jryannel/sqlb/issues/119) fixed the *silent* half |

An unsupported column type costs more than one column: the CHECKs and indexes
over it cannot be declared either. Three of the eight distinct skip messages
across studio-apps were cascades of this kind, so the counts above understate
their cost.

## How to find out where you stand

`sqlb-survey` reports all of this against a real database — what imports clean,
what imports partially, what the round trip fails to reproduce. It is read-only
against the source and takes seconds.

```bash
go run github.com/jryannel/sqlb/cmd/sqlb-survey@main "$SRC" "$SCRATCH" > survey.md
```

The scratch database must carry the same extensions as the source
([#115](https://github.com/jryannel/sqlb/issues/115)), and a project whose
migration runner keeps per-module bookkeeping tables needs `-exclude`
([#123](https://github.com/jryannel/sqlb/issues/123)).

## The rule these were triaged by

When a survey turns up a difference between a schema and what sqlb can declare,
one question decides which side moves:

> **A schema change must be defensible if sqlb vanished tomorrow.** Where it is
> not, sqlb moves instead — and where the shape is common across projects, sqlb
> moves regardless, because otherwise the tax is paid by every adopter.

Everything in *Enforced* and *Recommended* passes that test on its own merits.
Everything in *Gaps* fails it, which is why it is sqlb's problem and not yours.
