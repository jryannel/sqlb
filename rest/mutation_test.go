package rest_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/rest"
)

func completeMutationSpec() rest.MutationSpec {
	return rest.MutationSpec{
		Name:    "complete",
		Path:    "/posts/{id}/complete",
		Field:   "CompletePost",
		Summary: "Complete a post",
		Writes:  []string{"status", "title"},
		HasBody: true,
	}
}

func mountMutation(t *testing.T, db sqlb.Executor, spec rest.MutationSpec,
	do func(context.Context, *Post, CompletePost) error) humatest.TestAPI {
	t.Helper()
	_, api := humatest.New(t, huma.DefaultConfig("Test", "1.0.0"))
	if err := rest.Mutation[Post, CompletePost](api, db, postOptions(), spec, do); err != nil {
		t.Fatalf("mounting the mutation: %v", err)
	}
	return api
}

// Byte-for-byte the same envelope Action's item form gets: fetch, verb, write
// exactly the declared columns.
func TestMutationFetchesRunsAndWritesTheDeclaredColumns(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]any{postRow("p1", "Hello")}})

	var seen *Post
	api := mountMutation(t, db.db, completeMutationSpec(), func(_ context.Context, p *Post, in CompletePost) error {
		seen = p
		p.Status = "published"
		p.Title = "Hello, done"
		p.Body = "rewritten"
		return nil
	})

	resp := api.Post("/posts/p1/complete", map[string]any{"note": "shipped"})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}
	if seen == nil || seen.ID != "p1" {
		t.Fatalf("the verb was handed %+v, want the fetched row", seen)
	}

	stmt := db.lastStatement()
	set, _, _ := strings.Cut(stmt, " WHERE ")
	for _, want := range []string{`"status"`, `"title"`} {
		if !strings.Contains(set, want) {
			t.Errorf("update is missing %q:\n%s", want, set)
		}
	}
	if strings.Contains(set, `"body"`) {
		t.Errorf("the update wrote a column the mutation did not declare:\n%s", set)
	}
}

// A nil func is refused at mount, same as a missing action Do.
func TestMutationRefusesAMissingDo(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]any{postRow("p1", "Hello")}})
	_, api := humatest.New(t, huma.DefaultConfig("Test", "1.0.0"))
	err := rest.Mutation[Post, CompletePost](api, db.db, postOptions(), completeMutationSpec(), nil)
	if err == nil {
		t.Fatal("want an error mounting a mutation with a nil func")
	}
}
