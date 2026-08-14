package schema_test

import (
	"testing"

	"github.com/jryannel/sqlb/schema"
)

// tasksWithMutation builds a tasks table carrying one mutation, for the
// refusals below.
func tasksWithMutation(m schema.Mutation) *schema.Registry {
	r := schema.NewRegistry()
	r.Table("tasks",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("title"),
		schema.Enum("status", "open", "done").Default(schema.Value("open")),
	).Expose(schema.REST{Ops: schema.CRUD | schema.OpList}).AddMutation(m)
	return r
}

func TestAValidMutationPasses(t *testing.T) {
	r := tasksWithMutation(schema.Mutation{
		Name:   "complete",
		Body:   schema.Body(schema.Text("note").Nullable()),
		Writes: []string{"status"},
	})
	if err := r.Validate(); err != nil {
		t.Fatalf("a well-formed mutation was refused: %v", err)
	}
}

// The default path is the item form, matching Action's default.
func TestAMutationDefaultsToTheItemPath(t *testing.T) {
	r := tasksWithMutation(schema.Mutation{Name: "complete"})
	m := r.Get("tasks").Mutations()[0]

	if m.Path != "/{id}/complete" {
		t.Errorf("path = %q, want the item form", m.Path)
	}
	if got := m.FullPath("/tasks"); got != "/tasks/{id}/complete" {
		t.Errorf("full path = %q", got)
	}
}

// Unlike Action, a mutation with no {id} is refused: that shape is what
// schema.Action is for, and letting Mutation accept it too would make the
// split by name mean nothing.
func TestAMutationWithNoRowToAddressIsRefused(t *testing.T) {
	r := tasksWithMutation(schema.Mutation{Name: "sweep", Path: "/sweep"})
	if err := r.Validate(); err == nil {
		t.Fatal("expected the schema to be refused")
	}
}

func TestMutationWritesMustNameRealStorage(t *testing.T) {
	r := tasksWithMutation(schema.Mutation{Name: "complete", Writes: []string{"stauts"}})
	if err := r.Validate(); err == nil {
		t.Fatal("expected the schema to be refused")
	}
}

// A mutation and an action sharing a name would collide on one operation id.
func TestAMutationCollidingWithAnActionIsRefused(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("tasks", schema.UUIDv7("id").PrimaryKey()).
		Expose(schema.REST{Ops: schema.CRUD | schema.OpList}).
		AddAction(schema.Action{Name: "complete"}).
		AddMutation(schema.Mutation{Name: "complete"})

	if err := r.Validate(); err == nil {
		t.Fatal("expected the schema to be refused")
	}
}
