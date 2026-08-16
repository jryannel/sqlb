package sqlb_test

import (
	"strings"
	"testing"

	"github.com/jryannel/sqlb"
)

type oneToOneUser struct {
	ID      string           `db:"id" sqlb:"pk"`
	Profile *oneToOneProfile `db:"-" json:"profile,omitempty" sqlb:"expands=user_id,reverse"`
}

type oneToOneProfile struct {
	ID     string `db:"id" sqlb:"pk"`
	UserID string `db:"user_id"`
}

// The guard-proven-both-ways companion lives beside it: a plain forward
// relation and a capped collection must keep their existing SQL shape, so
// this new branch cannot be the only path exercised.
func TestReverseTagJoinsOnTheTargetsForeignKey(t *testing.T) {
	q := sqlb.Query[oneToOneUser]().Expand("profile")
	got, _, err := q.SQL()
	if err != nil {
		t.Fatalf("SQL() error: %v", err)
	}
	if !strings.Contains(got, `LEFT JOIN "one_to_one_profiles" AS "__ex_profile"`) {
		t.Errorf("missing the expected join:\n%s", got)
	}
	if !strings.Contains(got, `"__ex_profile"."user_id" = "one_to_one_users"."id"`) {
		t.Errorf("join condition should be target.FK = base.PK, got:\n%s", got)
	}
	if strings.Contains(got, "has_more") {
		t.Errorf("a one-to-one reverse relation must not use the capped-collection envelope:\n%s", got)
	}
}
