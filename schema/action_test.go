package schema_test

import (
	"strings"
	"testing"

	"github.com/jryannel/sqlb/schema"
)

// tasksWith builds a tasks table carrying one action, for the refusals below.
func tasksWith(a schema.Action) *schema.Registry {
	r := schema.NewRegistry()
	r.Table("tasks",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("title"),
		schema.Enum("status", "open", "done").Default(schema.Value("open")),
		schema.Computed("is_open", schema.TypeBool, schema.FromSQL("status = 'open'")),
	).Expose(schema.REST{Ops: schema.CRUD | schema.OpList}).Action(a)
	return r
}

// refusal asserts that validating r fails and that the message says why.
func refusal(t *testing.T, r *schema.Registry, wants ...string) {
	t.Helper()
	err := r.Validate()
	if err == nil {
		t.Fatal("expected the schema to be refused")
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v\nwant it to mention %q", err, want)
		}
	}
}

func TestAValidActionPasses(t *testing.T) {
	r := tasksWith(schema.Action{
		Name:   "complete",
		Body:   schema.Body(schema.Text("note").Nullable()),
		Writes: []string{"status"},
	})
	if err := r.Validate(); err != nil {
		t.Fatalf("a well-formed action was refused: %v", err)
	}
}

// The default path is the item form, which is what nearly every verb wants.
func TestAnActionDefaultsToTheItemPath(t *testing.T) {
	r := tasksWith(schema.Action{Name: "complete"})
	a := r.Get("tasks").Actions()[0]

	if a.Path != "/{id}/complete" {
		t.Errorf("path = %q, want the item form", a.Path)
	}
	if a.IsCollection() {
		t.Error("the default path is a collection action")
	}
	if got := a.FullPath("/tasks"); got != "/tasks/{id}/complete" {
		t.Errorf("full path = %q", got)
	}
}

// A write set that names nothing is a route answering 200 having written
// nothing, which is the least debuggable outcome available.
func TestWritesMustNameRealStorage(t *testing.T) {
	refusal(t, tasksWith(schema.Action{Name: "complete", Writes: []string{"stauts"}}),
		"Writes names no column")

	// A computed column has no storage to write to. The failure without this is
	// an UPDATE naming an expression, which the database rejects at request
	// time rather than at startup.
	refusal(t, tasksWith(schema.Action{Name: "complete", Writes: []string{"is_open"}}),
		"computed")

	// Re-keying a row is a delete and an insert, not a verb.
	refusal(t, tasksWith(schema.Action{Name: "complete", Writes: []string{"id"}}),
		"primary key")
}

// A collection action has no row, so a write set on one is a declaration that
// cannot be honoured.
func TestACollectionActionCannotDeclareAWriteSet(t *testing.T) {
	refusal(t, tasksWith(schema.Action{
		Name: "purge", Path: "/purge", Writes: []string{"status"},
	}), "no row to write")
}

// A body property is a value, not a column. Ignoring a capability it cannot
// have would leave a declaration reading as though it did something.
func TestBodyPropertiesCannotClaimColumnCapabilities(t *testing.T) {
	refusal(t, tasksWith(schema.Action{
		Name: "complete",
		Body: schema.Body(schema.Text("note").Filterable()),
	}), "Filterable", "describes a column rather than a request body")

	refusal(t, tasksWith(schema.Action{
		Name: "complete",
		Body: schema.Body(schema.Text("note").Hidden()),
	}), "Hidden")
}

// Two verbs on one path make routing depend on declaration order.
func TestTwoActionsCannotShareAPath(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("tasks",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("title"),
	).Expose(schema.REST{Ops: schema.OpRead}).
		Action(schema.Action{Name: "complete"}).
		Action(schema.Action{Name: "finish", Path: "/{id}/complete"})

	refusal(t, r, "same path")
}

func TestAnActionNeedsAnExposedTable(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("tasks",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("title"),
	).Action(schema.Action{Name: "complete"})

	refusal(t, r, "is not exposed", "add Expose")
}

// The verb is a URL segment and a Go identifier at once, so it has to survive
// being both.
func TestActionNamesAreConstrained(t *testing.T) {
	for _, bad := range []string{"Complete", "complete task", "-complete", "complete-", "1complete"} {
		r := tasksWith(schema.Action{Name: bad})
		if err := r.Validate(); err == nil {
			t.Errorf("action name %q was accepted", bad)
		}
	}
	// Hyphens in the middle are the spelling a path wants, and are accepted.
	if err := tasksWith(schema.Action{Name: "mark-read"}).Validate(); err != nil {
		t.Errorf("a hyphenated verb was refused: %v", err)
	}
}

// An item verb addresses a row by id, so there has to be something to address
// it by. Caught at the declaration rather than at mount, because the
// declaration is where the mistake is.
func TestAnItemActionNeedsAPrimaryKey(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("events",
		schema.Text("name").Filterable(),
	).Expose(schema.REST{Ops: schema.OpList}).
		Action(schema.Action{Name: "replay"})

	refusal(t, r, "no primary key")
}

// The manifest is what an agent reads to learn the API. A verb missing from it
// reads as a CRUD-only resource, and the transition gets guessed at.
// Touches is unenforced by design — the tables it names may belong to another
// module, and tracing what a Go func writes is not something this package can
// do. So validation refuses only what says nothing (#149).
func TestTouchesRefusesAClaimThatSaysNothing(t *testing.T) {
	refusal(t, tasksWith(schema.Action{
		Name:    "complete",
		Touches: []string{"comments", "comments"},
	}), "Touches names \"comments\" twice")

	refusal(t, tasksWith(schema.Action{
		Name:    "complete",
		Touches: []string{""},
	}), "Touches has an empty table name")
}

// A table this registry has never heard of is the ordinary case, not a typo:
// the point of the field is the cross-module write, and refusing an unknown
// name would refuse exactly the declaration worth making.
func TestTouchesAcceptsATableThisRegistryDoesNotHave(t *testing.T) {
	r := tasksWith(schema.Action{
		Name:    "complete",
		Writes:  []string{"status"},
		Touches: []string{"billing.invoices", "tasks"},
	})
	if err := r.Validate(); err != nil {
		t.Fatalf("a cross-module reach was refused: %v", err)
	}
}

// Unlike Writes, which needs a row, a collection action is the shape most
// likely to have a reach: it does all of its work through the transaction.
func TestACollectionActionMayDeclareAReach(t *testing.T) {
	r := tasksWith(schema.Action{
		Name:    "purge-archived",
		Path:    "/purge-archived",
		Touches: []string{"tasks", "comments"},
	})
	if err := r.Validate(); err != nil {
		t.Fatalf("a collection action's reach was refused: %v", err)
	}
}

func TestTheManifestCarriesTheVerbs(t *testing.T) {
	r := tasksWith(schema.Action{
		Name:    "complete",
		Body:    schema.Body(schema.Text("note").Nullable()),
		Writes:  []string{"status"},
		Touches: []string{"comments"},
	})
	var rest *schema.RESTManifest
	for _, tm := range r.BuildManifest().Tables {
		if tm.Name == "tasks" {
			rest = tm.REST
		}
	}
	if rest == nil || len(rest.Actions) != 1 {
		t.Fatalf("the manifest carries no actions: %+v", rest)
	}
	a := rest.Actions[0]
	switch {
	case a.Name != "complete":
		t.Errorf("name = %q", a.Name)
	case a.Path != "/tasks/{id}/complete":
		t.Errorf("path = %q", a.Path)
	case a.Method != "POST":
		t.Errorf("method = %q", a.Method)
	case len(a.Body) != 1 || a.Body[0].Name != "note" || !a.Body[0].Nullable:
		t.Errorf("body = %+v", a.Body)
	case len(a.Writes) != 1 || a.Writes[0] != "status":
		t.Errorf("writes = %v", a.Writes)
	// The manifest is what a program reads instead of making a request, so the
	// half the write set understates has to be in it too.
	case len(a.Touches) != 1 || a.Touches[0] != "comments":
		t.Errorf("touches = %v", a.Touches)
	}
}
