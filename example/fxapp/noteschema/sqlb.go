package noteschema

//go:generate go run github.com/jryannel/sqlb/cmd/sqlb generate .

import "github.com/jryannel/sqlb/codegen"

// SqlbProject tells `sqlb generate` what this example emits and where.
//
// example/fxapp is a module of its own, so paths resolve against
// example/fxapp rather than the repository root. The generated code lands in
// store/ rather than beside go.mod, because the module root here is the
// composition — app.go and its test — and a package that is half generated
// models and half fx wiring would make it harder, not easier, to see which is
// which.
//
// No TypeScript, Dart or CLI client: example/tasks emits all three and this
// one has nothing to add on that subject.
func SqlbProject() codegen.Project {
	return codegen.Project{
		Options: codegen.Options{
			Dir:     "store",
			Package: "store",
		},

		// MinPostgres(18) makes UUIDv7 primary keys default to the built-in
		// uuidv7(), so the migration applies to a stock postgres:18 with no
		// extension installed. cmd/migrate passes the same 18, and the two
		// have to agree: one history with two spellings of the same generator
		// is a history that only replays on the machine it was written on.
		MinPostgres: 18,

		MigrationsDir: "migrations",
	}
}
