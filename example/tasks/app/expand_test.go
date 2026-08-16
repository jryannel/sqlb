package app_test

import (
	"fmt"
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

// The reverse direction: a list and the tasks that point back at it. ADR-0022.
//
// This is the screen the forward direction could not serve. "A board of lists,
// each showing its first few tasks" was two requests per list and an N+1 the
// client had to write; it is now one request, and the cap is declared in the
// schema rather than negotiated per caller.
func TestExpandCollectsTheTasksOfAList(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")

	backlog := alice.listID("Backlog")
	done := alice.listID("Done")
	for i, title := range []string{"First", "Second", "Third"} {
		alice.taskID(backlog, title, map[string]any{"position": i})
	}

	got := alice.get("/lists?expand=tasks&sort=name").expect(http.StatusOK).list()
	if len(got.Items) != 2 {
		t.Fatalf("got %d lists, want 2: %s", len(got.Items), mustJSON(got.Items))
	}

	// A collection is an envelope rather than a bare array, because an array
	// cannot say it was truncated.
	tasks, ok := got.Items[0]["tasks"].(map[string]any)
	if !ok {
		t.Fatalf("no expanded tasks on the list: %s", mustJSON(got.Items[0]))
	}
	items, ok := tasks["items"].([]any)
	if !ok {
		t.Fatalf("the collection carries no items: %s", mustJSON(tasks))
	}
	if len(items) != 3 {
		t.Fatalf("got %d tasks, want 3: %s", len(items), mustJSON(items))
	}
	if tasks["has_more"] != false {
		t.Errorf("three tasks under a cap of twenty should not report has_more: %s", mustJSON(tasks))
	}

	// Ordered by position, as the schema declares.
	for i, want := range []string{"First", "Second", "Third"} {
		row, _ := items[i].(map[string]any)
		if row["title"] != want {
			t.Errorf("position %d = %v, want %q", i, row["title"], want)
		}
	}

	// A list with no tasks says so, rather than omitting the key or sending
	// null: "none" and "did not ask" are different answers.
	empty, ok := got.Items[1]["tasks"].(map[string]any)
	if !ok || got.Items[1]["id"] != done {
		t.Fatalf("the empty list did not expand: %s", mustJSON(got.Items[1]))
	}
	if rows, _ := empty["items"].([]any); len(rows) != 0 || empty["has_more"] != false {
		t.Errorf("a list with no tasks expanded to %s", mustJSON(empty))
	}
}

// The cap is the load-bearing half. Past it the response says so, and the
// caller follows the tasks endpoint filtered by the same foreign key — which is
// paging and filtering that already exist rather than a second surface.
func TestACollectionIsCappedAndSaysSo(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")

	backlog := alice.listID("Backlog")
	for i := range 25 { // the schema caps this relation at 20
		alice.taskID(backlog, fmt.Sprintf("Task %02d", i), map[string]any{"position": i})
	}

	got := alice.get("/lists/" + backlog + "?expand=tasks").expect(http.StatusOK).item()
	tasks, ok := got["tasks"].(map[string]any)
	if !ok {
		t.Fatalf("no expanded tasks on the list: %s", mustJSON(got))
	}
	items, _ := tasks["items"].([]any)
	if len(items) != 20 {
		t.Fatalf("got %d tasks, want the declared cap of 20", len(items))
	}
	if tasks["has_more"] != true {
		t.Errorf("a truncated collection did not report has_more: %s", mustJSON(tasks))
	}

	// The escape hatch the cap assumes: the rest are reachable through the
	// child's own endpoint, filtered by the column that collected them.
	rest := alice.get("/tasks?list_id=eq." + backlog + "&sort=position&page=2&per_page=20").
		expect(http.StatusOK).list()
	if len(rest.Items) != 5 {
		t.Errorf("the overflow is not reachable: got %d tasks on page 2", len(rest.Items))
	}
}

// The one-to-one direction: a unique FK's Inverse resolves to the target row
// or null, never the {items, has_more} envelope every other reverse relation
// in this schema uses. profiles.user_id carries a single-column Unique
// constraint, which is what makes taskschema.Profile's Inverse("profile") on
// User structurally one-to-one — see the design doc's "Compatibility"
// section for why this is a deliberate break of the Frozen list envelope.
func TestExpandOfAOneToOneRelationIsAnObjectNotAnEnvelope(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	alice.profileID(alice.userID, "Backend engineer.")

	got := alice.get("/users?expand=profile").expect(http.StatusOK).list()
	if len(got.Items) != 1 {
		t.Fatalf("got %d users, want 1: %s", len(got.Items), mustJSON(got.Items))
	}

	profile, ok := got.Items[0]["profile"].(map[string]any)
	if !ok {
		t.Fatalf("expected profile to be a plain object, got %T: %s",
			got.Items[0]["profile"], mustJSON(got.Items[0]))
	}
	if _, hasEnvelope := profile["items"]; hasEnvelope {
		t.Errorf("a one-to-one expansion must not use the {items, has_more} envelope: %s", mustJSON(profile))
	}
	if profile["bio"] != "Backend engineer." {
		t.Errorf("expansion did not carry the profile's columns: %s", mustJSON(profile))
	}
}

// A list still says "none" rather than omitting the key (TestTasksCarryNoListUnlessAsked
// covers "did not ask"); the one-to-one case adds a third answer a capped
// collection never needed: "asked, and there simply is no row."
//
// The key must be present with a null value, not simply missing — an omitted
// key and a key holding JSON null decode to the same Go nil through
// map[string]any, so a bare "!= nil" would pass whether or not the server
// sent the key at all. Checked with the two-value map read instead, which is
// the only way to tell "asked, and there is no row" (this test) from "did not
// ask" (the second half below, and TestTasksCarryNoListUnlessAsked).
func TestExpandOfAOneToOneRelationIsNullWhenAbsent(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	// alice's user has no profile row created for this test.

	got := alice.get("/users?expand=profile").expect(http.StatusOK).list()
	if len(got.Items) != 1 {
		t.Fatalf("got %d users, want 1: %s", len(got.Items), mustJSON(got.Items))
	}
	v, ok := got.Items[0]["profile"]
	if !ok {
		t.Fatalf(`expected "profile" to be present with a null value, but the key was absent: %s`,
			mustJSON(got.Items[0]))
	}
	if v != nil {
		t.Errorf("expected profile to be null when absent, got: %s", mustJSON(got.Items[0]))
	}

	// Without ?expand=profile the key is not there at all — the distinction
	// the assertion above exists to prove, mirroring the contract
	// TestTasksCarryNoListUnlessAsked keeps for the collection-shaped case.
	plain := alice.get("/users").expect(http.StatusOK).list()
	if len(plain.Items) != 1 {
		t.Fatalf("got %d users, want 1: %s", len(plain.Items), mustJSON(plain.Items))
	}
	if _, present := plain.Items[0]["profile"]; present {
		t.Errorf("the relation was serialised without being asked for: %s", mustJSON(plain.Items[0]))
	}
}

// profiles has no workspace_id (see the note on taskschema.Profile), so
// app/hooks.go scopes POST /profiles by looking the named user_id up through
// the already-scoped users query rather than by a predicate on profiles
// itself. This is the direct proof, the same case TestWorkspacesAreIsolated
// makes for every table that does have a workspace_id to filter by: Bob must
// not be able to plant a profile against a user in a workspace he does not
// share.
func TestProfileCreateIsScopedToTheCallersWorkspace(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	bob := account(t, server, "bob@example.com", "Globex")

	// Same answer a nonexistent user_id gets: 404, not 403 — Bob is not told
	// whether alice's id exists, only that his request matched nothing.
	bob.post("/profiles", map[string]any{
		"user_id": alice.userID,
		"bio":     "planted from another workspace",
	}).expect(http.StatusNotFound)

	// And nothing landed: Alice still has no profile to expand.
	got := alice.get("/users?expand=profile").expect(http.StatusOK).list()
	if v, ok := got.Items[0]["profile"]; !ok || v != nil {
		t.Errorf("a cross-workspace create should not have landed: %s", mustJSON(got.Items[0]))
	}
}

// taskschema.Profile exposes only OpCreate — the comment on it explains why:
// profiles has no workspace_id to scope a list by, so a served GET would leak
// across tenants the way TestProfileCreateIsScopedToTheCallersWorkspace's
// POST case once did. That decision lives in Ops: schema.OpCreate, prose a
// future schema edit could silently widen (adding OpRead/OpList back) without
// any generated surface objecting — this is the gate that would catch it: if
// GET /profiles or GET /profiles/{id} ever start responding, this fails.
func TestProfilesHasNoReadableEndpoint(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	profileID := alice.profileID(alice.userID, "Backend engineer.")

	resp := alice.get("/profiles")
	if resp.Code == http.StatusOK {
		t.Fatalf("GET /profiles answered 200; profiles must stay create-only, or its tenancy scope has no read-side check: %s", resp.Body)
	}

	resp = alice.get("/profiles/" + profileID)
	if resp.Code == http.StatusOK {
		t.Fatalf("GET /profiles/{id} answered 200; profiles must stay create-only, or its tenancy scope has no read-side check: %s", resp.Body)
	}
}

// One statement, whichever direction it runs in: a list expanding its tasks
// while a task expands its list is the same page count either way, and the
// collection must not multiply the rows it hangs off.
func TestACollectionDoesNotMultiplyTheListPage(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")

	backlog := alice.listID("Backlog")
	for i, title := range []string{"First", "Second", "Third"} {
		alice.taskID(backlog, title, map[string]any{"position": i})
	}

	plain := alice.get("/lists").expect(http.StatusOK).list()
	expanded := alice.get("/lists?expand=tasks&count=exact").expect(http.StatusOK).list()

	if len(plain.Items) != len(expanded.Items) {
		t.Errorf("expanding a collection changed the page from %d rows to %d",
			len(plain.Items), len(expanded.Items))
	}
	if expanded.Total == nil || *expanded.Total != len(plain.Items) {
		t.Errorf("count = %v, want %d", expanded.Total, len(plain.Items))
	}
}
