package ejected

import (
	"net/url"
	"strings"
	"testing"
)

// The exit refuses the oversized requests the API refused, which is the claim
// the README makes and the one the request budgets exist to keep.
//
// It holds for the offset budget only since #151. Limits had no MaxOffset field
// at all, so a client that had been getting a 400 for `?page=50000000` from the
// API got a plan for ten billion discarded rows from the handler that replaced
// it — and `?cursor` did not come out with the exit, so there is no cheaper
// spelling to send that client to.
func TestEjectedListEnforcesTheOffsetBudget(t *testing.T) {
	// posts declares MaxOffset: 5000 and DefaultPageSize: 20, so page 250
	// starts at 4980 and page 500 at 9980.
	if _, err := ParseList(url.Values{"page": {"250"}}, postColumns, postLimits); err != nil {
		t.Errorf("page 250 starts inside the budget: %v", err)
	}
	_, err := ParseList(url.Values{"page": {"500"}}, postColumns, postLimits)
	if err == nil {
		t.Fatal("page 500 starts past the declared budget of 5000 rows and was accepted")
	}
	if !strings.Contains(err.Error(), "offset budget") {
		t.Errorf("the refusal does not say what was exceeded: %v", err)
	}

	// A page number near the top of int64 is the case the check is computed in
	// int64 for: (n-1)*per_page overflows into a negative offset, which fails
	// at the database rather than at validation.
	if _, err := ParseList(url.Values{"page": {"9223372036854775807"}}, postColumns, postLimits); err == nil {
		t.Error("a page number that overflows the offset was accepted")
	}
}

// A resource that declared no ceiling is held to the package default rather
// than to none, which is what a resolved zero has always meant for the other
// four budgets.
func TestEjectedOffsetBudgetHasADefault(t *testing.T) {
	// The default page size is 25, so page 4000 starts at 99_975 — just inside
	// the default budget of 100_000, and past every declared one in this schema.
	if _, err := ParseList(url.Values{"page": {"4000"}}, orgColumns, orgLimits); err != nil {
		t.Errorf("page 4000 at the default page size is inside the default budget: %v", err)
	}
	if _, err := ParseList(url.Values{"page": {"5000000"}}, orgColumns, orgLimits); err == nil {
		t.Error("a resource declaring no ceiling was left with no ceiling")
	}
}
