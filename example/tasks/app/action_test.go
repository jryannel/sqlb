package app_test

import (
	"net/http"
	"testing"
)

// POST /tasks/{id}/complete against a real Postgres (ADR-0043).
//
// Everything asserted here is the *envelope* rather than the verb, because the
// verb is eight lines in actions.go and the envelope is what the schema
// declaration bought. In order: the route exists at all, the write set is
// exactly what was declared, the transition holds the check constraint the
// table carries, the workspace boundary survives a route the hooks were not
// written for, and a failing verb takes its side effects down with it.

func TestCompletingATaskWritesBothColumns(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	list := alice.listID("Work")
	task := alice.taskID(list, "Ship it", nil)

	body := alice.post("/tasks/"+task+"/complete", map[string]any{}).
		expect(http.StatusOK).item()

	// The response is the row as the write left it, which is what makes this a
	// verb rather than a fire-and-forget.
	if body["status"] != "done" {
		t.Errorf("status = %v, want done", body["status"])
	}
	// The column the client cannot set — it is ReadOnly, so it is absent from
	// both request bodies — moved anyway, because the envelope wrote it on the
	// server's authority.
	if body["completed_at"] == nil {
		t.Error("completed_at is still null after completing the task")
	}
	// And the state the check constraint forbids never existed: had the two
	// columns not been written together, this request would have failed on the
	// constraint rather than answering 200.
}

// The note is not in the write set. It is the verb's own business, and the verb
// writes it through the transaction the envelope opened — which is the answer
// ADR-0043 gives to "what about a verb that touches another table".
func TestACompletionNoteLandsInTheSameTransaction(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	list := alice.listID("Work")
	task := alice.taskID(list, "Ship it", nil)

	alice.post("/tasks/"+task+"/complete", map[string]any{"note": "shipped on friday"}).
		expect(http.StatusOK)

	comments := alice.get("/comments?task_id=eq." + task).expect(http.StatusOK).list()
	if len(comments.Items) != 1 {
		t.Fatalf("got %d comments, want the one the verb wrote: %v", len(comments.Items), comments.Items)
	}
	if comments.Items[0]["body"] != "shipped on friday" {
		t.Errorf("comment body = %v", comments.Items[0]["body"])
	}
	// The workspace_id the comment needed came from the BeforeCreate hook,
	// which ran because the verb's insert went through the same executor
	// everything else does. Nothing in actions.go mentions a workspace.
	if comments.Items[0]["workspace_id"] == nil {
		t.Error("the comment has no workspace, so the hook did not run on the verb's insert")
	}
}

// The escape hatch, end to end: a *rest.Problem returned by the verb keeps its
// status, and the transition does not happen.
func TestCompletingATwiceDoneTaskIsRefused(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	list := alice.listID("Work")
	task := alice.taskID(list, "Ship it", nil)

	alice.post("/tasks/"+task+"/complete", map[string]any{}).expect(http.StatusOK)

	p := alice.post("/tasks/"+task+"/complete", map[string]any{"note": "again"}).
		expect(http.StatusConflict).problem()
	if p.Detail != "the task is already done" {
		t.Errorf("detail = %q, want the verb's own", p.Detail)
	}

	// And the refusal rolled back: no second comment, because the whole call
	// was one unit of work and the verb failed inside it.
	comments := alice.get("/comments?task_id=eq." + task).expect(http.StatusOK).list()
	if len(comments.Items) != 0 {
		t.Errorf("a refused verb left %d comments behind", len(comments.Items))
	}
}

// The safety claim. An action's fetch runs the model's BeforeQuery hook, so a
// route nobody wrote a scope for is scoped anyway — which is the failure class
// ADR-0030 closed for CRUD, closed here for the routes where hand-written
// handlers actually live.
func TestAnActionCannotReachAnotherWorkspacesRow(t *testing.T) {
	server := newServer(t, freshDB(t))
	alice := account(t, server, "alice@example.com", "Acme")
	bob := account(t, server, "bob@example.com", "Globex")

	task := alice.taskID(alice.listID("Acme work"), "Acme secret", nil)

	// 404, not 403: the row was never in the query Bob's request issued, so
	// there is nothing to be forbidden from. Same answer GET /tasks/{id} gives,
	// and for the same reason.
	bob.post("/tasks/"+task+"/complete", map[string]any{}).expect(http.StatusNotFound)

	// Alice's task is untouched.
	if got := alice.get("/tasks/" + task).expect(http.StatusOK).item(); got["status"] != "todo" {
		t.Errorf("status = %v, want todo: another workspace moved it", got["status"])
	}
}
