// Package introspect reads a Postgres schema out of pg_catalog and returns the
// same *schema.Registry the DSL produces.
//
// That symmetry is the point. A registry from here can be handed to
// migrate.Diff as the current state, which makes generating a migration and
// adopting an existing database the same machinery pointed in opposite
// directions (ADR-0014). It is also the only way to check the diff engine
// against a real database: render a schema to DDL, apply it, read it back, and
// the diff between what went in and what came out must be empty.
//
// # Why this is not in migrate
//
// migrate does not connect to a database and says so in its own documentation:
// it produces files, and a runner applies them. This package does connect, so
// it is separate, and migrate stays a pure function over two data structures.
//
// # The connection
//
// Everything here works through a sqlb.Executor, so a pool, a connection or a
// transaction the caller already holds all work, and reading a catalog uses the
// same handle as querying a table (ADR-0040).
//
// # What cannot be represented
//
// The DSL is narrower than Postgres, and the failure that matters is dropping
// something quietly — a schema that looks complete, describes the database
// incorrectly, and produces a migration that reverses work nobody meant to
// reverse. So every construct this cannot express is collected into a Report
// rather than skipped. Read it. A Report with entries means the emitted schema
// is not the whole database.
package introspect
