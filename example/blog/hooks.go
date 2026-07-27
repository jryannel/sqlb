package blog

import (
	"context"

	"github.com/jryannel/sqlb"
)

// Hand-written domain rules, in a file the generator does not touch.
//
// RegisterHooks installs the predicate that makes schema.SoftDelete mean
// something. The schema adds posts.deleted_at and stops there — nothing in the
// runtime reads the column — so hiding the deleted rows is a registration, and
// BeforeQuery is where it goes: one registration constrains every read of Post,
// including the ones the generated REST handlers issue. That is the argument of
// ADR-0008, and this is what it looks like applied.
//
// It is an exported function rather than an init so the registration is visible
// at the call site, and so a test can choose not to make it.
//
// Tenant scoping belongs on the same hook — `q.Where(sqlb.F("org_id").Eq(org))`,
// reading the org out of ctx. It is left out here because the example has no
// authentication to read it from, not because it is a separate mechanism.
func RegisterHooks() {
	sqlb.On[Post]().BeforeQuery(func(_ context.Context, q *sqlb.Builder[Post]) error {
		q.Where(sqlb.F("deleted_at").IsNull())
		return nil
	})
}
