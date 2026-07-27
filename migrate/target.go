package migrate

// This file is about one question: which Postgres will the generated migration
// actually run on?
//
// Almost nothing here needs to ask. The DDL sqlb emits is ordinary SQL that has
// been valid for a decade. The exception is a default whose *generator* arrived
// in a particular release, where writing the modern spelling produces a
// migration that fails on an older server, and writing the old one produces a
// migration that needs an extension installed.
//
// Note where this lives. Options, which Render and Write take, is too late:
// by then the SQL is a string and the choice has already been made. So this is
// an option on Diff, which is what turns two registries into statements.

// Option configures Diff.
type Option func(*diffOptions)

type diffOptions struct {
	// minPG is the oldest Postgres major version the output must run on, or 0
	// when the caller has not said.
	minPG int
}

// MinPostgres declares the oldest Postgres major version the generated
// migration has to run on, which lets the DDL layer use a built-in where one
// exists instead of requiring an extension.
//
// Today it changes exactly one thing. schema.GenUUIDv7 emits
// uuid_generate_v7(), which is the pg_uuidv7 extension's spelling — so a
// migration for a UUIDv7 primary key does not apply to a stock Postgres at all.
// Postgres 18 has uuidv7() built in, and MinPostgres(18) emits that instead.
//
// Unset means the old spelling, which is the behaviour every migration
// generated before this option existed already has. A default that silently
// changed emitted DDL would be the one mistake ADR-0014 says is not recoverable
// by regenerating.
//
// Pass it consistently across a project. Generating one migration with it and
// the next without leaves a table whose columns default through two different
// spellings of the same generator — harmless to the database, confusing to
// read, and a diff will not flag it because both import back to the same
// schema.GenUUIDv7.
func MinPostgres(major int) Option {
	return func(o *diffOptions) { o.minPG = major }
}

// builtinDefaults maps a generator's canonical spelling — the one
// schema.GenUUIDv7 and friends produce, and the one introspect reads back — to
// a built-in available from some major version onward.
//
// A list rather than a capability system, because it has one entry and an
// abstraction whose second implementation is hypothetical constrains the first
// for no benefit (ADR-0015 rejected one for the same reason). Add the second
// entry before generalising.
var builtinDefaults = []struct {
	canonical string
	builtin   string
	since     int
}{
	{canonical: "uuid_generate_v7()", builtin: "uuidv7()", since: 18},
}

// resolve returns the spelling to emit for a raw default expression.
//
// Anything unrecognised is returned untouched: schema.Expr takes arbitrary SQL,
// and rewriting something this does not understand is exactly the guessing this
// project refuses elsewhere.
func (o diffOptions) resolve(raw string) string {
	for _, b := range builtinDefaults {
		if raw == b.canonical && o.minPG >= b.since {
			return b.builtin
		}
	}
	return raw
}
