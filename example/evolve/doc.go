// Package evolve is a schema that changed five times, and the machinery that
// kept the changes honest.
//
// The other examples show a schema at rest. This one is about what happens to
// one over time — which is where a data layer either helps or gets in the way,
// and where the interesting failures are not "this does not compile" but "this
// deleted a column of production data" and "this shipped a clean migration and
// broke every client".
//
// docs/refactoring-a-database.md is the narrative. What is here:
//
//	evolveschema/schema.go   the current state, and only the current state
//	migrations/              how it got there, in the order it happened
//	history_test.go          what revisions 4 and 5 did to the API
//
// # The shape, and why it is this shape
//
// There is no v1 package and no v2 package. A real project has one schema file
// that it edits, and a directory of migrations it has already applied to
// databases it cannot go back and change — so that is what this is. The cost is
// that no intermediate state is readable as Go; the benefit is that the example
// is arranged the way the thing it teaches actually is.
//
// The five revisions, and what each was chosen to show:
//
//	00010  initial_schema            the baseline
//	00020  safe_additions            a column with a default, a table, an index
//	00021  safe_additions_indexes    ...which needed a file of its own
//	00030  widen_ticket_subject      varchar(80) → text, free in this direction
//	00040  rename_email_and_agents   a column and a table, both declared
//	00050  drop_legacy_ref           destructive, and rendered only under a flag
//
// Revision 2 rendering two files is not a mistake. An index on a live table is
// created CONCURRENTLY, which cannot run inside a transaction, so putting it
// with the others would have removed the rollback guarantee from all of them.
// migrate.Split is what separates them, and the versions here are spaced by ten
// so a revision that splits has somewhere to put the extra file.
//
// # What checks what
//
// Three gates, because a schema edit can go wrong in three unrelated ways:
//
//   - `mise run generate-check` — the generated code still matches the
//     declaration. Catches a forgotten `sqlb generate`.
//   - `mise run impact-check` — the REST contract has not changed since the
//     committed baseline. Catches a wire break, which a clean migration and a
//     clean regeneration will both happily pass.
//   - pgtest/evolve — the migration history, replayed into an empty database,
//     still builds what evolveschema declares. Catches the one thing no file
//     comparison can: schema.go edited with no migration written. It needs a
//     database, so it lives in the module that has one.
//
// The second and third are the pair worth understanding, because revision 4 is
// the case where they disagree. Renaming customers.email to email_address is a
// single clean ALTER TABLE ... RENAME COLUMN that loses no data, so the replay
// gate is satisfied. It is also a 400 for every client still sending
// ?email=…, which is what impact-check is for. Neither gate can see what the
// other sees.
package evolve
