# ADR-0019: Connections go through PgBouncer, except the ones that cannot

- **Status:** Working — the query path assumes nothing session-scoped, and
  `pgtest` runs it through a real PgBouncer in transaction pooling mode
- **Confidence:** High on the carve-out list, which is tested rather than reasoned
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-30 (read against ADR-0040)

## Context

The target deployment runs PgBouncer in **transaction pooling** mode. sqlb
assumed a direct connection everywhere and never said so — an assumption that is
invisible in the code, because sqlb takes an `Executor` from the caller and would
not notice a pooled one until it misbehaved in production.

Transaction pooling returns the server connection at transaction end, so anything
whose state outlives a transaction breaks: `LISTEN` for the change feed
([ADR-0012](0012-change-feed-outbox.md)), `CREATE INDEX CONCURRENTLY` and session
advisory locks in migration runners, and pgx v5's prepared-statement cache — the
last being the entire query path, not a corner.

**Measured**, in `pgtest/pgbouncer_test.go`, against PgBouncer 1.24.1 in
transaction pooling in front of Postgres 18 with pgx v5.10 at defaults:

| Claim | Result |
|---|---|
| The query path works through the pooler | **Yes**, at pgx's defaults |
| `LISTEN` survives the pooler | **No.** Accepted, and the notification never arrives |
| `NOTIFY` survives the pooler | **Yes**, including inside a transaction |

Two details matter more than the yes/no. The query path works because PgBouncer
1.24.1 defaults `max_prepared_statements` to 200 — a deployment setting, not a
property of pgx; below 1.21 or at 0, every query fails. And a pooled `LISTEN` is
*accepted and silently useless*, which makes the misconfiguration below a real
risk rather than a theoretical one.

## Decision

**The pooler is the default path, and the query path may assume nothing
session-scoped.** No feature may rely on a `SET` outliving its transaction, a
session advisory lock, a temp table, or a cursor held across transactions.

**Two components connect direct, and are named rather than discovered:** the
change-feed dispatcher's `LISTEN` connection, and the migration runner. `NOTIFY`
is transactional and fire-and-forget, so it works from any pooled connection —
the blast radius is one connection, not the application. `introspect` issues
ordinary queries and needs no exception; it is listed to record that it was
considered.

**sqlb does not manage this.** It takes a handle from the caller and will not
grow a pooled-versus-direct abstraction. What it owes users is documentation of
which component needs which connection, not a seam that pretends to arrange it —
a pooler-aware sqlb would be deciding a deployment topology it cannot see.

**pgx is the assumed driver**, and under
[ADR-0040](0040-the-driver-is-a-dependency.md) the depended-upon one. Owning
connections stays refused on its own merits, not because of the old
stdlib-only invariant.

## Consequences

**Buys.** The assumption is visible and testable instead of latent. The carve-outs
are two connections in a deployment, not a constraint on the application.
Forbidding session state is good hygiene anyway — it is what makes the query path
horizontally scalable with or without a pooler.

**Costs.** Two connection paths to operate, and a misconfiguration is *silent*:
point the dispatcher at the pooler and it never wakes. ADR-0012's fallback poll
turns that into latency rather than data loss, which also means it can hide the
mistake indefinitely. The prepared-statement mode is a per-deployment setting
sqlb can neither see nor verify. `pgtest` now needs a second container.

## What would change our mind

- Deployment moves to session pooling or a pooler that supports `LISTEN` — every
  carve-out collapses and this record shrinks to a paragraph.
- A deployment turns up on a PgBouncer that does not track prepared statements —
  the query path needs pgx in exec mode. Under ADR-0040 that becomes a
  `QueryExecMode` sqlb sets in code rather than a DSN every consumer must get
  right, which makes a wrong default sqlb's to own.
- A second component needs session state. Two named exceptions is a list; four is
  a missing abstraction.
- Generated writes now open a transaction
  ([ADR-0021](0021-hooks-receive-an-event.md)), so a server connection is held
  for the whole `BEGIN`…`COMMIT`. That occupancy change is unmeasured — if
  `avg_xact_time` diverges from `avg_query_time` under load, look here first;
  `rest.Options.DisableTransactions` is the per-resource lever.
- Someone points the dispatcher at the pooler and nobody notices for a week — the
  fallback poll is masking a misconfiguration, and the dispatcher needs a startup
  assertion.
- A `LISTEN` through the pooler starts arriving. `pgtest` asserts it does not, and
  its failure message says this would be good news, so nobody deletes the test to
  make it pass.

## Cost of change

Cheap now — nothing is built against it, and the change is mostly which
connection a component is handed. Expensive in one direction: if the change feed
ships assuming a direct `LISTEN` and the deployment later exposes only a pooler,
polling becomes the only option. The fallback poll is what makes that a
degradation rather than a rewrite. Reversing the no-session-state rule is the
expensive one the other way, since finding what quietly depends on session state
means auditing the whole query path.

## Revisions

- 2026-07-27 — Written, after PgBouncer turned out to be part of the target
  deployment and appeared nowhere in the codebase or the record.
- 2026-07-27 — Measured against PgBouncer 1.24.1 and Postgres 18. Two findings
  the documented version did not have: the query path survives only because the
  pooler tracks prepared statements, and a pooled `LISTEN` fails silently.
- 2026-07-30 — Rewrote the exec-mode trigger under ADR-0040. Nothing measured
  changes — the carve-outs are a property of transaction pooling, not the driver.
- 2026-07-30 — Condensed.
