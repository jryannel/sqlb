// Package migrate turns a schema change into migration files for an existing
// migration runner.
//
// There are three layers. Diff compares two schema registries and returns the
// Changes between them. The DDL layer renders those changes as Postgres
// statements. A Format renders a set of changes as the files a particular
// runner expects. They are separable on purpose: the first is a pure function
// over two data structures, the second knows only Postgres, and the third
// knows only goose or golang-migrate.
//
// sqlb does not apply migrations and does not track which have run. Projects
// already have a runner — goose, golang-migrate, atlas, a shell script — and
// replacing a working one is a far larger ask than adopting a code generator,
// for no benefit sqlb could offer. This package produces files; your runner
// applies them.
//
// Goose is the default because it is what this project's authors use, and
// because its single-file Up/Down format is the one most likely to be pasted
// into by hand afterwards.
package migrate
