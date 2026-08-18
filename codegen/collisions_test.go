package codegen_test

import (
	"strings"
	"testing"

	"github.com/jryannel/sqlb/codegen"
	"github.com/jryannel/sqlb/schema"
)

// kanbanFixture is the collision as it was reported: a boards table and a
// board_columns table, whose singularised name is BoardColumn — which is also
// what the <Entity>Column convention calls Board's selectable-column union
// (#261).
func kanbanFixture() *schema.Registry {
	r := schema.NewRegistry()
	boards := r.Table("boards",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("name").Sortable(),
	).Expose(schema.REST{Ops: schema.OpRead | schema.OpList})

	r.Table("board_columns",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Ref("board", boards).Filterable(),
		schema.Text("title"),
	).Expose(schema.REST{Ops: schema.OpRead | schema.OpList})
	return r
}

// The generator refuses rather than writing a file tsc will not compile.
//
// It used to write it and report success, so the first sign of trouble was two
// "Duplicate identifier" errors naming neither the schema nor the two tables
// that produced them. Everything in the message below is here because the
// emitted file does not carry it: the identifier, what each table contributed,
// and that a table's TypeScript names come from its own name.
func TestTSCollisionIsRefused(t *testing.T) {
	_, err := codegen.Generate(codegen.Options{
		Registry: kanbanFixture(), Dir: t.TempDir(), Package: "gen", TSDir: "web/api",
	})
	if err == nil {
		t.Fatal("a schema that generates two BoardColumn declarations was accepted")
	}
	for _, want := range []string{
		"BoardColumn",
		"the row type of board_columns",
		"the selectable-column type of boards",
		"TS2300",
		"Rename one of the tables",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%v", want, err)
		}
	}
}

// The Dart client has the same collision from the same conventions: a class
// against an enum where TypeScript had an interface against a union. Fixed in
// the same commit because a guard on one client would leave a project that
// generates both with the same silent invalid output, one language over.
func TestDartCollisionIsRefused(t *testing.T) {
	_, err := codegen.Generate(codegen.Options{
		Registry: kanbanFixture(), Dir: t.TempDir(), Package: "gen", DartDir: "dart/api",
	})
	if err == nil {
		t.Fatal("a schema that generates two BoardColumn declarations was accepted")
	}
	for _, want := range []string{
		"BoardColumn",
		"the row type of board_columns",
		"the selectable-column type of boards",
		"one top-level namespace",
		"Rename one of the tables",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%v", want, err)
		}
	}
}

// And it refuses only that. A schema whose names do not collide generates, so
// the guard is not a rule against tables sharing a prefix: board_columns beside
// kanban_boards is the same pair of tables, renamed the way the message says.
func TestTSNamesThatDoNotCollideAreFine(t *testing.T) {
	r := schema.NewRegistry()
	boards := r.Table("kanban_boards",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("name").Sortable(),
	).Expose(schema.REST{Ops: schema.OpRead | schema.OpList})

	r.Table("board_columns",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Ref("board", boards).Filterable(),
		schema.Text("title"),
	).Expose(schema.REST{Ops: schema.OpRead | schema.OpList})

	files := generateTS(t, r)
	client := files["client.gen.ts"]
	for _, want := range []string{
		"export interface BoardColumn {",
		"export type KanbanBoardColumn =",
	} {
		if !strings.Contains(client, want) {
			t.Errorf("the client is missing %q", want)
		}
	}
}
