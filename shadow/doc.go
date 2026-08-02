// Package shadow builds a schema by replaying a migration history into an
// empty database, and reading back what the migrations actually produced.
//
// It answers the question migrate.Diff needs answered and cannot answer itself:
// what is the current schema? Reading production is the obvious source and the
// worse one. It tells you what the database looks like, not whether the
// migration history produces it — so a hand-applied hotfix, a migration edited
// after it ran, or a statement someone skipped are all invisible, and the next
// generated migration is computed against a state no migration file describes
// (ADR-0014).
//
// Replaying into a scratch database is a different claim: this is the schema
// the checked-in history builds. Comparing that with production is drift
// detection, and it needs no extra API — it is migrate.Diff between the two
// registries, and an empty result is the claim that the history and the
// database agree.
//
// # This is not a migration runner
//
// sqlb does not apply migrations, and this does not change that. A runner
// tracks which migrations have run, applies the outstanding ones to a database
// people depend on, and must never get it wrong. This applies all of them, in
// order, to an empty database nobody depends on, and throws away the result.
// The two have almost nothing in common except the word "apply".
//
// What follows from that: no version table is read or written, nothing is
// skipped, and Down sections are never executed.
//
// # The database is yours
//
// Build takes a connection to an empty database and will not create or drop
// one. Creating databases needs credentials beyond what the rest of sqlb asks
// for, and dropping the wrong one is unrecoverable — so the destructive half of
// "scratch database" stays with the caller, who knows which ones are scratch.
package shadow
