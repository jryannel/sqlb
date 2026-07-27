package app_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestTheWholeLoop is the demo, as an assertion: register, get a token, make
// two lists, put tasks in them, and query the result with the filter grammar.
//
// Nothing in the request path below was hand-written. The endpoints, their
// OpenAPI parameters, the request bodies, the filtering, the sorting and the
// pagination all come from taskschema/schema.go.
func TestTheWholeLoop(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")

	backlog := alice.listID("Backlog")
	shipping := alice.listID("Shipping")

	due := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	alice.taskID(backlog, "Write the migration", map[string]any{"priority": "high"})
	alice.taskID(backlog, "Review the schema", map[string]any{"priority": "low"})
	alice.taskID(shipping, "Cut the release", map[string]any{"priority": "urgent", "due_at": due})

	// The columns are Filterable and Sortable because the schema says so, and
	// only because of that.
	got := alice.get("/tasks?list_id=" + backlog + "&sort=title").expect(http.StatusOK).list()
	if len(got.Items) != 2 {
		t.Fatalf("filtering by list returned %d tasks, want 2: %s", len(got.Items), mustJSON(got.Items))
	}
	if got.Items[0]["title"] != "Review the schema" {
		t.Errorf("sort=title did not order by title: %v", titles(got.Items))
	}

	// Operator form, and repeated parameters conjoining.
	urgent := alice.get("/tasks?priority=in.high,urgent&sort=-priority").expect(http.StatusOK).list()
	if len(urgent.Items) != 2 {
		t.Errorf("priority=in.high,urgent returned %d tasks, want 2", len(urgent.Items))
	}

	// Search fans out over the columns marked Searchable — title and
	// description — and no others.
	found := alice.get("/tasks?search=migration").expect(http.StatusOK).list()
	if len(found.Items) != 1 || found.Items[0]["title"] != "Write the migration" {
		t.Errorf("search=migration returned %v", titles(found.Items))
	}

	// Paging reports has_more without counting, unless asked.
	page := alice.get("/tasks?per_page=2&sort=title").expect(http.StatusOK).list()
	if len(page.Items) != 2 || !page.HasMore {
		t.Errorf("per_page=2: got %d items, has_more=%v", len(page.Items), page.HasMore)
	}
	if page.Total != nil {
		t.Errorf("total was counted without being asked for: %v", *page.Total)
	}
	counted := alice.get("/tasks?per_page=2&count=exact").expect(http.StatusOK).list()
	if counted.Total == nil || *counted.Total != 3 {
		t.Errorf("count=exact returned total %v, want 3", counted.Total)
	}
}

// TestWorkspacesAreIsolated is the claim the whole example is arranged around.
//
// Two tenants, one database, one set of handlers, and no handler that knows
// about tenants. Everything below is enforced by the hooks in hooks.go.
func TestWorkspacesAreIsolated(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	bob := account(t, server, "bob@example.com", "Globex")

	aliceList := alice.listID("Acme work")
	aliceTask := alice.taskID(aliceList, "Acme secret", nil)

	bobList := bob.listID("Globex work")
	bob.taskID(bobList, "Globex secret", nil)

	// Reads: Bob sees his own row and no others. Not "Bob is refused" — the
	// query never had Alice's rows in it.
	bobsTasks := bob.get("/tasks").expect(http.StatusOK).list()
	if len(bobsTasks.Items) != 1 || bobsTasks.Items[0]["title"] != "Globex secret" {
		t.Fatalf("Bob's task list = %v", titles(bobsTasks.Items))
	}

	// Reading a specific row by id: 404, the same answer as an id that does not
	// exist. Telling the two apart would confirm the row exists.
	bob.get("/tasks/" + aliceTask).expect(http.StatusNotFound)

	// Writes are scoped by a separate hook, because a BeforeQuery predicate says
	// nothing about what an UPDATE may reach.
	bob.patch("/tasks/"+aliceTask, map[string]any{"title": "owned"}).expect(http.StatusNotFound)
	bob.delete("/tasks/" + aliceTask).expect(http.StatusNotFound)

	// And Alice's task is untouched.
	after := alice.get("/tasks/" + aliceTask).expect(http.StatusOK).item()
	if after["title"] != "Acme secret" {
		t.Errorf("the task was modified across the boundary: %v", after)
	}

	// Workspaces and users are scoped too, by predicates of their own — the
	// workspace by identity, users through their memberships.
	spaces := bob.get("/workspaces").expect(http.StatusOK).list()
	if len(spaces.Items) != 1 || spaces.Items[0]["slug"] != "globex" {
		t.Errorf("Bob can see workspaces other than his own: %v", spaces.Items)
	}
	users := bob.get("/users").expect(http.StatusOK).list()
	if len(users.Items) != 1 || users.Items[0]["email"] != "bob@example.com" {
		t.Errorf("Bob can see users outside his workspace: %v", users.Items)
	}
}

// TestUnauthenticatedRequestsAreRefused checks both halves of the defence: the
// middleware, and the hooks behind it.
func TestUnauthenticatedRequestsAreRefused(t *testing.T) {
	server := newServer(t, freshDB(t))
	anon := &client{t: t, h: server}

	for _, path := range []string{"/tasks", "/lists", "/workspaces", "/users", "/auth/me"} {
		resp := anon.get(path)
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a token = %d, want 401: %s", path, resp.Code, resp.Body)
		}
		// RFC 6750: say which scheme to use.
		if got := resp.Headers.Get("WWW-Authenticate"); got == "" {
			t.Errorf("GET %s returned a 401 with no WWW-Authenticate header", path)
		}
	}

	// The public routes stay reachable, or the API could not be entered at all.
	anon.get("/health").expect(http.StatusOK)
	anon.get("/openapi.json").expect(http.StatusOK)

	// A token signed with the wrong key is not merely unparsed — it verifies
	// and fails, which is the case worth having a test for.
	forged := &client{t: t, h: server, token: forgedToken(t)}
	forged.get("/tasks").expect(http.StatusUnauthorized)
}

// TestRejectionsSayWhatWouldHaveWorked is ADR-0011 over HTTP, and the check
// that a hidden column stays hidden even in a diagnostic.
func TestRejectionsSayWhatWouldHaveWorked(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")

	// description is Searchable and not Sortable.
	p := alice.get("/tasks?sort=description").expect(http.StatusBadRequest).problem()
	if len(p.Errors) != 1 {
		t.Fatalf("want one rejection, got %d: %s", len(p.Errors), mustJSON(p))
	}
	if len(p.Errors[0].Allowed) == 0 {
		t.Fatalf("the rejection names no alternatives: %+v", p.Errors[0])
	}
	if !contains(p.Errors[0].Allowed, "title") {
		t.Errorf("title is Sortable but is not in the allow-list: %v", p.Errors[0].Allowed)
	}

	// Every problem at once, not one per round trip.
	both := alice.get("/tasks?sort=description&nonexistent=1").
		expect(http.StatusBadRequest).problem()
	if len(both.Errors) < 2 {
		t.Errorf("two bad parameters produced %d errors: %s", len(both.Errors), mustJSON(both))
	}

	// password_hash is Hidden. It must appear in no response, no parameter, no
	// allow-list and no schema — a diagnostic that names it is an oracle for
	// what the resource is concealing.
	users := alice.get("/users").expect(http.StatusOK)
	if strings.Contains(string(users.Body), "password_hash") {
		t.Error("a hidden column reached a response body")
	}
	bad := alice.get("/users?sort=password_hash").expect(http.StatusBadRequest)
	if strings.Contains(string(bad.Body), "password_hash") &&
		strings.Contains(string(bad.Body), `"allowed"`) {
		for _, e := range bad.problem().Errors {
			if contains(e.Allowed, "password_hash") {
				t.Error("a hidden column was named in a rejection's allow-list")
			}
		}
	}
	doc := alice.get("/openapi.json").expect(http.StatusOK)
	if strings.Contains(string(doc.Body), "password_hash") {
		t.Error("a hidden column appears in the OpenAPI document")
	}
}

// TestCommentIsOneUnitOfWork covers the generated create and the two hooks that
// give it an invariant: the comment and the task's counter move together, and
// neither moves if the other cannot.
//
// There is no hand-written handler behind this any more. `rest` wraps a
// generated write in a transaction, so the hooks receive a context carrying it
// — which is what lets the rule live on the model rather than on a route.
func TestCommentIsOneUnitOfWork(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	list := alice.listID("Backlog")
	task := alice.taskID(list, "Needs discussion", nil)

	for i := range 3 {
		alice.post("/comments", map[string]any{
			"task_id": task,
			"body":    "comment " + string(rune('a'+i)),
		}).expect(http.StatusCreated)
	}

	got := alice.get("/tasks/" + task).expect(http.StatusOK).item()
	if got["comment_count"] != float64(3) {
		t.Errorf("comment_count = %v, want 3", got["comment_count"])
	}

	comments := alice.get("/comments?task_id=" + task).expect(http.StatusOK).list()
	if len(comments.Items) != 3 {
		t.Errorf("listed %d comments, want 3", len(comments.Items))
	}
	// The author came from the token, not from the request body — which has no
	// author_id field, because the column is ReadOnly.
	me := alice.get("/auth/me").expect(http.StatusOK).item()
	user, _ := me["user"].(map[string]any)
	if comments.Items[0]["author_id"] != user["id"] {
		t.Errorf("author_id = %v, want the caller %v", comments.Items[0]["author_id"], user["id"])
	}

	// A comment on a task in another workspace is a 404 rather than the 500 the
	// composite foreign key alone would produce, and it leaves nothing behind:
	// the check and the two writes are one unit, so a failure at the first means
	// the other two never happened.
	bob := account(t, server, "bob@example.com", "Globex")
	bob.post("/comments", map[string]any{"task_id": task, "body": "nope"}).
		expect(http.StatusNotFound)

	still := alice.get("/tasks/" + task).expect(http.StatusOK).item()
	if still["comment_count"] != float64(3) {
		t.Errorf("comment_count moved on a failed create: %v", still["comment_count"])
	}
}

// TestCompletedAtFollowsStatus checks the trigger that cmd/migrate adds, and
// with it the check constraint the schema declares.
func TestCompletedAtFollowsStatus(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	list := alice.listID("Backlog")
	task := alice.taskID(list, "Finish the demo", nil)

	fresh := alice.get("/tasks/" + task).expect(http.StatusOK).item()
	if fresh["completed_at"] != nil {
		t.Errorf("a new task has completed_at = %v", fresh["completed_at"])
	}

	// completed_at is ReadOnly, so the request cannot set it — and does not have
	// to. The trigger fills it in, which is what keeps the check constraint
	// satisfiable through the generated PATCH.
	done := alice.patch("/tasks/"+task, map[string]any{"status": "done"}).
		expect(http.StatusOK).item()
	if done["completed_at"] == nil {
		t.Fatalf("status=done left completed_at null: %v", done)
	}

	reopened := alice.patch("/tasks/"+task, map[string]any{"status": "todo"}).
		expect(http.StatusOK).item()
	if reopened["completed_at"] != nil {
		t.Errorf("reopening left completed_at set: %v", reopened["completed_at"])
	}
}

// TestSoftDeleteHidesTheRow covers the hand-written DELETE and the read hook
// that gives it effect. Either half alone does nothing useful.
func TestSoftDeleteHidesTheRow(t *testing.T) {
	db := freshDB(t)
	server := newServer(t, db)
	alice := account(t, server, "alice@example.com", "Acme")
	list := alice.listID("Backlog")
	keep := alice.taskID(list, "Keep", nil)
	drop := alice.taskID(list, "Drop", nil)

	alice.delete("/tasks/" + drop).expect(http.StatusNoContent)

	remaining := alice.get("/tasks").expect(http.StatusOK).list()
	if len(remaining.Items) != 1 || remaining.Items[0]["id"] != keep {
		t.Fatalf("after a soft delete the list is %v", titles(remaining.Items))
	}
	alice.get("/tasks/" + drop).expect(http.StatusNotFound)

	// Deleting twice is a 404 rather than a second stamp that moves the
	// deletion time.
	alice.delete("/tasks/" + drop).expect(http.StatusNotFound)

	// The row is still there. A soft delete that actually removed the row would
	// pass every assertion above.
	var deleted int
	if err := db.QueryRow(
		`SELECT count(*) FROM tasks WHERE id = $1 AND deleted_at IS NOT NULL`, drop,
	).Scan(&deleted); err != nil {
		t.Fatalf("counting the deleted row: %v", err)
	}
	if deleted != 1 {
		t.Errorf("the row was hard-deleted: found %d marked rows", deleted)
	}
}

// TestATaskCannotJoinAnotherWorkspacesList is the composite foreign key.
//
// The hooks cannot catch this one. Alice's request is authenticated, scoped to
// her workspace, and names a list id she is not supposed to know — a value the
// BeforeCreate hook has no reason to question. What refuses it is
// tasks_list_in_same_workspace, added by cmd/migrate, and this test is the
// reason that constraint exists.
func TestATaskCannotJoinAnotherWorkspacesList(t *testing.T) {
	db := freshDB(t)
	server := newServer(t, db)
	alice := account(t, server, "alice@example.com", "Acme")
	bob := account(t, server, "bob@example.com", "Globex")

	bobsList := bob.listID("Globex work")

	resp := alice.post("/tasks", map[string]any{
		"list_id":     bobsList,
		"title":       "smuggled in",
		"description": "",
	})
	if resp.Code < 400 {
		t.Fatalf("a cross-workspace list reference was accepted: %d %s", resp.Code, resp.Body)
	}

	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM tasks`).Scan(&rows); err != nil {
		t.Fatalf("counting tasks: %v", err)
	}
	if rows != 0 {
		t.Errorf("the task was written anyway: %d rows", rows)
	}
}

// TestRolesAreCheckedOnInvite covers the one place a role is read for a
// decision rather than carried along.
func TestRolesAreCheckedOnInvite(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	bob := account(t, server, "bob@example.com", "Globex")

	bobID := id(t, bob.get("/auth/me").expect(http.StatusOK).item()["user"].(map[string]any))

	// Alice registered, so she owns Acme and may invite.
	alice.post("/memberships", map[string]any{"user_id": bobID, "role": "member"}).
		expect(http.StatusCreated)

	// Bob can now sign in to Acme, and does so as a member.
	anon := &client{t: t, h: server}
	token := anon.post("/auth/login", map[string]any{
		"email":     "bob@example.com",
		"password":  "correct-horse-battery-staple",
		"workspace": "acme",
	}).expect(http.StatusOK).item()
	if token["role"] != "member" {
		t.Fatalf("Bob signed in to Acme as %v, want member", token["role"])
	}

	bobAtAcme := &client{t: t, h: server, token: token["token"].(string)}
	// A member sees the workspace's memberships and may not add to them.
	bobAtAcme.get("/memberships").expect(http.StatusOK)
	bobAtAcme.post("/memberships", map[string]any{"user_id": bobID, "role": "owner"}).
		expect(http.StatusForbidden)
}

// TestLoginDoesNotLeakWhichAccountsExist covers the two answers that must be
// the same answer.
func TestLoginDoesNotLeakWhichAccountsExist(t *testing.T) {
	server := newServer(t, freshDB(t))
	account(t, server, "alice@example.com", "Acme")
	anon := &client{t: t, h: server}

	unknown := anon.post("/auth/login", map[string]any{
		"email":    "nobody@example.com",
		"password": "correct-horse-battery-staple",
	}).expect(http.StatusUnauthorized)

	wrong := anon.post("/auth/login", map[string]any{
		"email":    "alice@example.com",
		"password": "wrong-horse-battery-staple",
	}).expect(http.StatusUnauthorized)

	if string(unknown.Body) != string(wrong.Body) {
		t.Errorf("the two failures are distinguishable:\n  unknown: %s\n  wrong:   %s",
			unknown.Body, wrong.Body)
	}

	// Registering the same address twice is a 409 rather than a second account.
	dup := anon.post("/auth/register", map[string]any{
		"name":      "Impostor",
		"email":     "alice@example.com",
		"password":  "correct-horse-battery-staple",
		"workspace": "Not Acme",
	})
	dup.expect(http.StatusConflict)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func titles(items []map[string]any) []any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		out = append(out, it["title"])
	}
	return out
}
