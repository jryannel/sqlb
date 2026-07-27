# ADR-0019: Connections go through PgBouncer, except the ones that cannot

- **Status:** Exploring
- **Confidence:** Low
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

The target deployment runs PgBouncer in front of Postgres, in **transaction
pooling** mode. Session pooling would make most of this record unnecessary, but
it also gives up the connection multiplexing that is the reason to deploy a
pooler at all, so transaction pooling is what we assume.

Until now sqlb assumed a direct connection everywhere, and never said so. That
assumption is invisible in the code because sqlb does not own connections — it
takes an `Executor` from the caller — which means nothing in the codebase would
notice being handed a pooled one until it misbehaved in production.

Transaction pooling returns the server connection to the pool at transaction
end. Anything whose state outlives a transaction therefore breaks, and three
parts of this project depend on exactly that:

- `LISTEN` needs a session that survives past commit. It is how
  [ADR-0012](0012-change-feed-outbox.md)'s dispatcher avoids polling, and
  [ADR-0001](0001-postgres-only.md) cites `LISTEN/NOTIFY` as one of the features
  justifying Postgres-only.
- `CREATE INDEX CONCURRENTLY` cannot run inside a transaction block, and
  migration runners typically take *session* advisory locks to serialise
  themselves. The entire `Unblock` apparatus in `migrate` — the concurrent index
  build, the `NOT VALID` plus `VALIDATE` split — is built for a connection that
  behaves like a session.
- pgx v5 defaults to caching prepared statements, which is a documented
  incompatibility with transaction pooling below PgBouncer 1.21. This one is not
  a corner: it is the entire query path.

**Nothing here has been measured.** Everything above comes from documentation
and reasoning. In this record's company that is unusually weak evidence —
[ADR-0014](0014-migrations-and-import.md) measured every lock claim it makes
against a real Postgres — and it is why Confidence is Low rather than Medium.

## Decision

**The pooler is the default path, and sqlb's query path may assume nothing
session-scoped.** Reads, writes, REST handlers and hooks all run through
PgBouncer. No feature may rely on a `SET` outliving its transaction, a session
advisory lock, a temp table, or a cursor held across transactions.

**Three components connect direct, and are named rather than discovered:**

1. **The change-feed dispatcher's `LISTEN` connection** (ADR-0012). Note the
   asymmetry that makes this cheap: `NOTIFY` is transactional and fire-and-forget,
   so it works fine through the pooler from whichever connection did the
   mutation. Only `LISTEN` needs the carve-out. The blast radius is one
   connection, not the application.
2. **The migration runner**, for the concurrent-index and advisory-lock reasons
   above. sqlb emits DDL and does not apply it, so this is a constraint on the
   caller's runner, and it belongs in the generated file header rather than only
   here.
3. **Nothing else.** `introspect` issues ordinary queries and works through the
   pooler; it is listed only to record that it was considered and does not need
   the exception.

**sqlb does not manage this.** It takes `*sql.DB` from the caller and will not
grow a pooled-versus-direct abstraction. Owning connections would mean importing
a driver, which is precisely what `deps-check` exists to prevent
([ADR-0015](0015-module-isolation.md) is about a different kind of module, but
the same instinct). What sqlb owes its users is documentation of which component
needs which connection — not a seam that pretends to arrange it.

**pgx is the assumed driver.** Not a dependency of the engine, but the driver
the `pgtest` module uses and the one whose defaults the documentation should
speak to.

## Consequences

**What this buys.** The assumption becomes visible and testable instead of
latent. The carve-outs are two connections in a deployment, not a constraint on
the application. And forbidding session state in the query path is good hygiene
on its own terms — it is what makes the query path horizontally scalable whether
or not a pooler is present.

**What this costs.** Two connection paths to configure and operate, and a
misconfiguration is *silent*: point the dispatcher at the pooler and it simply
never wakes. ADR-0012's fallback poll exists so that this degrades to latency
rather than data loss, which means the fallback is also capable of hiding the
mistake indefinitely. The prepared-statement mode is a per-deployment setting
sqlb can neither see nor verify. And the `pgtest` gate now needs a second
container and a network, which makes it slower and more Docker-dependent than a
single-Postgres gate would have been.

## What would change our mind

- **Deployment moves to session pooling, or to a pooler that supports `LISTEN`**
  (pgcat, supavisor). Every carve-out above collapses and this record shrinks to
  a paragraph.
- **The statement-cache setting costs measurable throughput** against exec mode.
  That is a real number, and this record should carry it rather than the current
  hand-wave.
- **A second component turns out to need session state.** Two named exceptions
  is a list; four is a missing abstraction, and the answer would then be a real
  seam rather than more prose.
- **Someone points the dispatcher at the pooler and nobody notices for a week.**
  That means the fallback poll is masking a misconfiguration rather than
  tolerating a fault, and the dispatcher needs a startup assertion that its
  connection can actually hold a `LISTEN`.
- **Any claim in the Context section fails when measured.** They are documented,
  not observed, and this project's standard is observation.

## Cost of change

**Cheap now**, and this is the reason to write it before the change feed exists.
Nothing is built against it, and the change is mostly *which connection a
component is handed* — configuration, not code.

**Expensive in one specific direction.** If the change feed ships assuming it
can hold a direct `LISTEN`, and the deployment later moves somewhere that only
exposes a pooler — which several managed Postgres offerings do — then the
dispatcher has no way to bypass it. Polling becomes the only option, which is
ADR-0012's explicitly rejected alternative returning as the sole survivor. The
fallback poll is what makes that a degradation rather than a rewrite, which is
most of its justification.

Reversing the no-session-state rule in the query path is the expensive one in
the other direction: it is cheap to keep and costly to reintroduce, because
finding out which feature quietly started depending on session state means
auditing the whole query path.

## Alternatives considered

**No pooler; connect direct everywhere.** Simplest, and what the code silently
assumed until this record. Lost to the deployment reality rather than on merit —
it remains the right model for a small single-process consumer, and such a
consumer can ignore every carve-out here.

**Session pooling.** Preserves `LISTEN`, prepared statements and advisory locks,
and would delete most of this document. Genuinely close, and the honest reason
it loses is that it gives up most of the connection multiplexing that justifies
running a pooler — not that anything here is impossible under it.

**sqlb owns the pool and routes session-needing work to a direct connection
itself.** Attractive, because it would make the carve-outs automatic instead of
operational. Rejected: sqlb takes `*sql.DB` from the caller precisely so the
driver stays the caller's ([ADR-0014](0014-migrations-and-import.md)), and
owning connections would make the engine inherit a Postgres driver that every
consumer then inherits too.

**Drop `LISTEN/NOTIFY` and poll.** Rejected by ADR-0012 on the latency-versus-load
curve. Worth noting it is not gone — it is the fallback, running slowly.

## Revisions

- 2026-07-27 — Written, after PgBouncer turned out to be part of the target
  deployment and appeared nowhere in the codebase or the record.
