package app_test

import (
	"net/http"
	"testing"
)

// TestExpandJoinsTheList is the first time the expansion SQL meets a real
// Postgres.
//
// Everything else about ?expand is proven against the in-memory driver, which
// will accept any string the builder hands it. json_build_object over the
// target's columns, the LEFT JOIN and the CASE WHEN that keeps a missing row
// null are all *plausible* until Postgres parses them — so this test exists for
// the parser as much as for the result.
//
// Nothing below was hand-written either: `list` is expandable because
// taskschema declares schema.Ref("list", List).Expandable(), and the generated
// model carries the typed field the row lands in.
func TestExpandJoinsTheList(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")

	backlog := alice.listID("Backlog")
	alice.taskID(backlog, "Write the migration", nil)

	got := alice.get("/tasks?expand=list").expect(http.StatusOK).list()
	if len(got.Items) != 1 {
		t.Fatalf("got %d tasks, want 1: %s", len(got.Items), mustJSON(got.Items))
	}

	list, ok := got.Items[0]["list"].(map[string]any)
	if !ok {
		t.Fatalf("no expanded list on the task: %s", mustJSON(got.Items[0]))
	}
	if list["id"] != backlog {
		t.Errorf("expanded the wrong list: %s", mustJSON(list))
	}
	if list["name"] != "Backlog" {
		t.Errorf("expansion did not carry the list's columns: %s", mustJSON(list))
	}
	// The key is still there. Expansion adds the row, it does not replace the
	// reference — a client that only wanted the id should not have to reach
	// into the object for it.
	if got.Items[0]["list_id"] != backlog {
		t.Errorf("the foreign key went missing: %s", mustJSON(got.Items[0]))
	}
}

// The join puts a second table in the statement, and both tables have a
// `position`, a `description` and a `workspace_id`. Postgres will not guess
// which one an unqualified reference means — it refuses the statement with
// SQLSTATE 42702 — so every column a request can name has to be qualified once
// anything is joined in.
//
// This is the failure the fake driver cannot see: it accepts whatever string
// the builder hands it, so each of the three below returned a page against the
// in-memory tests and a 500 against Postgres.
func TestExpandDoesNotMakeTheOtherParametersAmbiguous(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")

	backlog := alice.listID("Backlog")
	alice.taskID(backlog, "Write the migration", map[string]any{"description": "the schema one"})

	for _, q := range []string{
		"/tasks?expand=list&sort=position",   // ORDER BY: lists.position too
		"/tasks?expand=list&search=schema",   // WHERE: lists.description too
		"/tasks?expand=list&count=exact",     // the count query joins as well
		"/tasks?expand=list&select=id,title", // an explicit projection
	} {
		got := alice.get(q).expect(http.StatusOK).list()
		if len(got.Items) != 1 {
			t.Errorf("%s returned %d tasks, want 1: %s", q, len(got.Items), mustJSON(got.Items))
		}
	}
}

// Expansion is what a request asks for, not what a resource always does. A
// list endpoint that joined unconditionally would make every caller pay for a
// relation most of them do not read.
func TestTasksCarryNoListUnlessAsked(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	alice.taskID(alice.listID("Backlog"), "Write the migration", nil)

	got := alice.get("/tasks").expect(http.StatusOK).list()
	if _, present := got.Items[0]["list"]; present {
		t.Errorf("the relation was serialised without being asked for: %s", mustJSON(got.Items[0]))
	}
}

// A relation the schema did not mark Expandable is refused, and the rejection
// says what would have worked — the same contract every other rejection here
// keeps. `assignee` is a real reference on tasks; it is simply not expandable.
func TestUnknownExpandIsRefusedWithTheAllowedList(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")

	p := alice.get("/tasks?expand=assignee").expect(http.StatusBadRequest).problem()
	var allowed []string
	for _, e := range p.Errors {
		allowed = append(allowed, e.Allowed...)
	}
	if !contains(allowed, "list") {
		t.Errorf("the rejection should name the expandable relations, got %s", mustJSON(p))
	}
}

// The item endpoint addresses its row by primary key, and `id` is the column
// tasks and lists most obviously share. Unqualified, `WHERE "id" = $1` beside a
// LEFT JOIN is not a predicate that matches the wrong row — Postgres refuses
// the statement (SQLSTATE 42702). Only a real database can say so.
func TestExpandOnTheItemEndpoint(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")

	backlog := alice.listID("Backlog")
	task := alice.taskID(backlog, "Write the migration", nil)

	got := alice.get("/tasks/" + task + "?expand=list").expect(http.StatusOK).item()

	list, ok := got["list"].(map[string]any)
	if !ok {
		t.Fatalf("no expanded list on the task: %s", mustJSON(got))
	}
	if list["id"] != backlog || list["name"] != "Backlog" {
		t.Errorf("expanded the wrong list: %s", mustJSON(list))
	}
	if got["list_id"] != backlog {
		t.Errorf("the foreign key went missing: %s", mustJSON(got))
	}
}

// Without the parameter the item endpoint is what it was: no join, no relation.
func TestItemCarriesNoListUnlessAsked(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	task := alice.taskID(alice.listID("Backlog"), "Write the migration", nil)

	got := alice.get("/tasks/" + task).expect(http.StatusOK).item()
	if _, present := got["list"]; present {
		t.Errorf("the relation was serialised without being asked for: %s", mustJSON(got))
	}
}

// The item endpoint refuses an unexpandable relation the same way the list one
// does — same status, same allow-list. It is the same rejection, not a second
// copy of it.
func TestItemExpandRejectionMatchesTheListEndpoint(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	task := alice.taskID(alice.listID("Backlog"), "Write the migration", nil)

	item := alice.get("/tasks/" + task + "?expand=assignee").expect(http.StatusBadRequest).problem()
	list := alice.get("/tasks?expand=assignee").expect(http.StatusBadRequest).problem()

	if len(item.Errors) != 1 || len(list.Errors) != 1 {
		t.Fatalf("expected one error each:\nitem %s\nlist %s", mustJSON(item), mustJSON(list))
	}
	if item.Errors[0].Message != list.Errors[0].Message {
		t.Errorf("messages differ: item %q, list %q", item.Errors[0].Message, list.Errors[0].Message)
	}
	if !contains(item.Errors[0].Allowed, "list") {
		t.Errorf("the item rejection does not say what would have worked: %s", mustJSON(item))
	}
}

// The item endpoint still refuses what it does not declare. `sort` is a real
// parameter on the list endpoint, which is what makes it the interesting one to
// try here.
func TestItemStillRefusesAnUnknownQueryParameter(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	task := alice.taskID(alice.listID("Backlog"), "Write the migration", nil)

	if r := alice.get("/tasks/" + task + "?sort=title"); r.Code == http.StatusOK {
		t.Errorf("an unknown query parameter was accepted on the item endpoint: %s", r.Body)
	}
}
