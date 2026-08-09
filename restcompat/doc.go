// Package restcompat diffs the REST contract two schemas generate, and
// classifies each delta as breaking, additive, or neutral for a deployed
// client. It is the engine behind `sqlb impact`
// (ADR-0039: a schema edit is an API edit, and the break is diffed).
//
// It is a sibling of migrate.Diff, not a consumer of it. migrate.Diff reads the
// columns and types that produce DDL and ignores capabilities, because
// capabilities emit no SQL. This reads the capabilities — Filterable, the Op
// set, exposure — precisely because the sharpest API breaks produce no DDL at
// all: un-exposing a column, dropping an operation, or a rename that is a clean
// migration and a wire break at the same time. So the two functions run over the
// same pair of registries and read different projections of them.
//
// Like migrate.Diff it is a pure function over two *schema.Registry values, with
// no database and no running server, which is what makes it golden-testable.
//
// Most of the contract is per resource and per column, and one part of it is
// not: the schema's WireCase spells every field of every resource, so a change
// to it is a rename of the whole API with no column renamed and no DDL emitted
// (ADR-0036). It is captured once per snapshot and reported as one break with no
// resource, which sorts above everything else.
//
// # What is deliberately honest here
//
// A change that is compatible for a reader and breaking for a writer — a column
// going NOT NULL widens the create body's required set while leaving responses
// untouched — is reported as two separate breaks, one per side, never folded
// into one. A classifier that reported the reader side and forgot the writer
// side would be a guard that fires sometimes, which reads as coverage it does
// not have (ADR-0016). Where a type change cannot be classified confidently in
// both directions, it is reported LevelUnknown rather than guessed as neutral.
package restcompat
