# Architecture Decision Records

An ADR records a decision that shaped this codebase, and — more importantly —
*why*, so that a future reader can tell whether the reasoning still holds.

## These are living documents

An ADR here is **not** a contract, a sign-off, or a historical artefact. It is
the current best understanding of a problem and the solution we have chosen for
it. Understanding changes. When it does, the ADR changes with it.

This is a deliberate departure from the original ADR convention, where an
accepted record is immutable and a change of mind means writing a new record
that supersedes it. That model optimises for an audit trail. We are optimising
for a document that is *true today*, because the main reader is someone — or
some agent — trying to work out how the system fits together right now, and a
directory of mostly-obsolete records serves them badly.

None of which makes changing one free. A decision that code has been built on
has a price to revise: work to redo, clients to migrate, sometimes data to move.
That price is real and should be named rather than discovered.

But a price is not a veto. If someone has a good reason and is willing to pay
the cost, that is the system working — the record exists to make the cost
visible so the trade can be made deliberately, not to make the answer permanent.
Every record therefore carries a **Cost of change** section alongside **What
would change our mind**: one names the trigger to reconsider, the other names
the bill.

So:

- **Edit ADRs in place** when your understanding improves. Add a line to the
  Revisions section saying what changed and why.
- **Reversing a decision is normal**, not a failure. It means you learned
  something. Record what you learned.
- **Weigh the cost, then decide.** The bar is a good reason and a willingness to
  pay, not consensus or seniority.
- **Watch for asymmetry.** Many decisions here are cheap to widen and expensive
  to narrow again. Those are the ones to change slowly, and the Cost of change
  section calls them out.
- **Write them early**, while the reasoning is fresh and before the decision
  feels inevitable. An ADR written after the fact tends to justify rather than
  explain.
- **Do not add process.** No approvals, no quorum, no status meetings. If you
  changed something architectural, write down why.

Only write a new record instead of editing when the *problem* changed, not the
answer — that is genuinely a different decision, and the old one stays as
context with its status set to Replaced.

## When to write one

Write an ADR when a choice is hard to reverse, has real trade-offs, or will
otherwise be re-litigated every few months by someone who does not know why it
went the way it did. Signals: you argued about it, you rejected a reasonable
alternative, or the answer is going to surprise someone.

Do not write one for choices with an obvious default, or for things the code
already says plainly. Those belong in a comment.

## Status

Status describes maturity and confidence, not approval:

| Status | Meaning |
|---|---|
| **Exploring** | Direction chosen, not yet built enough to trust. Expect movement. |
| **Working** | Implemented, and holding up in practice so far. |
| **Revisiting** | Something has challenged it. Actively being reconsidered. |
| **Replaced** | The problem changed. Points at the record that replaced it, and stays for the reasoning trail. |

There is deliberately no "Accepted" and no "Final".

**Confidence** is separate from status and is about how much evidence we have:
High (built it, used it, it held), Medium (built it, limited use), Low (reasoned
about it, have not felt the consequences yet). Being honest here is the whole
point — an ADR marked Working / Low confidence is a useful signal, not an
embarrassment.

## Reviewing

Each record carries a **Last reviewed** date. When you touch an area, glance at
its ADR: if it still reads true, bump the date; if it does not, fix it. An ADR
whose review date is far behind the code is a stale document, and stale
documents are worse than absent ones.

The **What would change our mind** section is where a record earns its keep.
It names the observation that should trigger a revisit. If you hit one of those
conditions, that is your cue to reopen the record — not to work around it
quietly.

## Writing one

Copy [`0000-template.md`](0000-template.md), take the next free number, and use
a short kebab-case slug: `0013-cursor-pagination.md`. Numbers are allocated in
order and never reused.

**Keep it short.** A page is usually enough, and a record nobody finishes reading
has failed at its only job. The sections are Context, Decision, Consequences,
What would change our mind, Cost of change, Revisions — and that is the whole
set. There is deliberately no *Alternatives considered*: a record is here to say
what we are doing and what it costs, not to re-argue the case against every path
not taken. Where a rejected option is genuinely close, name it in a line inside
the section it bears on, and move on.

## Index

| # | Title | Status | Confidence |
|---|---|---|---|
| [0001](0001-postgres-only.md) | Target Postgres only | Working | High |
| [0002](0002-queries-are-values.md) | Queries are values, not statements | Working | High |
| [0003](0003-one-ast-two-producers.md) | One predicate AST, two producers | Working | High |
| [0004](0004-schema-as-go-dsl.md) | Schema is a Go DSL, and codegen flows from it | Working | High |
| [0005](0005-runtime-query-engine.md) | The query engine is reflective, not generated | Working | Medium |
| [0006](0006-capabilities-are-opt-in.md) | Column capabilities are opt-in | Working | High |
| [0007](0007-generated-rest-handlers.md) | One generic handler, OpenAPI generated per resource | Working | Medium |
| [0008](0008-hooks-as-domain-seam.md) | Hooks are the domain-logic seam | Working | Medium |
| [0009](0009-typed-column-facade.md) | A generated typed column facade; predicates stay untyped | Working | Medium |
| [0010](0010-codegen-is-optional.md) | Code generation is optional | Working | Medium |
| [0011](0011-actionable-errors.md) | Rejections name what would have been accepted | Working | High |
| [0012](0012-change-feed-outbox.md) | Change notification via a transactional outbox † | Exploring | Low |
| [0013](0013-no-internal-split.md) | No public/internal package split | Working | Medium |
| [0014](0014-migrations-and-import.md) | Migrations by diff, adoption by import | Working | High |
| [0015](0015-module-isolation.md) | Modules own their tables, and do not reference each other's | Working | Medium |
| [0016](0016-guards-proven-both-ways.md) | A guard is not trusted until it has failed on purpose | Working | High |
| [0017](0017-enums-as-text-and-check.md) | An enum is text with a CHECK constraint | Working | Medium |
| [0018](0018-tooling-scoped-to-tracked-files.md) | Repository tooling operates on the files git tracks | Working | Medium |
| [0019](0019-pgbouncer-in-the-path.md) | Connections go through PgBouncer, except the ones that cannot | Working | High |
| [0020](0020-transaction-scoped-handle.md) | The transaction handle is built now, not with Go 1.27 | Working | Medium |
| [0021](0021-hooks-receive-an-event.md) | A hook gets a transaction, not an event | Working | High |
| [0022](0022-references-declare-their-inverse.md) | A reference declares the name its target knows it by | Working | Medium |
| [0023](0023-mixins-carry-behaviour.md) | A mixin contributes columns; carrying behaviour needs codegen | Working | High |
| [0024](0024-no-annotation-slot.md) | No annotation slot until something can consume one | Working | Medium |
| [0025](0025-expansion-is-one-statement.md) | Expansion is one statement, and Hidden survives the join | Working | Medium |
| [0026](0026-vectors-declare-their-index.md) | A vector column declares its index, and search is its own operation † | Working (column) / Exploring (index) | Medium |
| [0027](0027-keyset-pagination.md) | A page is a position, not a distance | Working | High |
| [0028](0028-typescript-client.md) | The TypeScript client is generated from the model, and stops at the query key | Working | Medium |
| [0029](0029-go-cli.md) | The CLI is generated too, and its help text is the type system | Working | Medium |
| [0030](0030-declared-scope-is-required.md) | A declaration that rows are confined is an obligation, not a comment | Working | High |
| [0031](0031-dart-client.md) | The Dart client keeps the vocabulary and gives up the narrowing | Working | Medium |
| [0032](0032-sqlb-command.md) | The command compiles a driver, and the project declares itself in Go | Working | Medium |
| [0033](0033-array-columns.md) | An array is its element type plus a flag, and the slice stays plain | Working | High |
| [0034](0034-one-column-addresses-a-row.md) | A row is addressed by one column, and a composite key becomes a unique index | Working | Medium |
| [0035](0035-type-overrides.md) | A type override changes the Go type and nothing else | Working | High |
| [0036](0036-the-wire-is-the-column-name.md) | The wire spells a column the way the schema does | Working | High |
| [0037](0037-search-is-ilike-until-it-cannot-be.md) | `?search` is ILIKE, and a `tsvector` column is not in 1.0 † | Working | High |
| [0038](0038-collections-are-flat.md) | A collection has one path, and the parent is a filter | Working | High |
| [0039](0039-a-schema-edit-is-an-api-edit.md) | A schema edit is an API edit, and the break is diffed | Exploring | Medium |
| [0040](0040-the-driver-is-a-dependency.md) | The driver is a dependency, not a seam | Accepted | High |
| [0041](0041-computed-fields.md) | A computed field is an expression in the row, and the parameterised ones oblige a hook | Working | Medium |

† **Deliberately not in 1.0.** The decision is recorded; the feature is out of
scope for the first tag. [The road to 1.0](../release-1.0.md) says why for each.
Scope is not a status — a record can be Working as a decision and unbuilt as a
feature, which 0037 is.

No record now obligates work before the tag. 0040 did — it breaks `Executor`,
which `compatibility.md` freezes, so it was land-before-1.0-or-never — and it is
built.
