package main

import (
	"strings"
	"testing"

	"github.com/jryannel/sqlb/introspect"
)

// The survey itself needs two databases and is exercised by running it; what is
// covered here is everything around that — the argument handling that decides
// whether it runs at all, and the pure functions that shape the report.

// The verb that made this one command rather than two. Reaching survey at all
// means the dispatch runs *before* run's package-argument handling, which would
// otherwise read the second DSN as a package pattern and hand it to `go list`.
func TestSurveyIsNotTreatedAsAPackageVerb(t *testing.T) {
	code, out := invoke(t, "survey", "postgres:///src", "postgres:///dst", "extra")
	if code != 2 {
		t.Fatalf("survey with three positional arguments exited %d, want 2:\n%s", code, out)
	}
	if strings.Contains(out, "go list") || strings.Contains(out, "matched no packages") {
		t.Errorf("survey was routed through the package resolver, so its DSNs were read as "+
			"a package pattern:\n%s", out)
	}
	if !strings.Contains(out, "<src-migrated-dsn>") {
		t.Errorf("the misuse did not print survey's own usage, which is the only place the "+
			"two DSNs are described:\n%s", out)
	}
}

// The top-level usage has to list survey, because a verb absent from `sqlb
// help` is one nobody finds — which is the state the separate binary was in.
func TestUsageListsSurvey(t *testing.T) {
	code, out := invoke(t, "help")
	if code != 0 {
		t.Fatalf("help exited %d:\n%s", code, out)
	}
	for _, want := range []string{"sqlb survey", "-modules", "-exclude"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage does not mention %q:\n%s", want, out)
		}
	}
}

// The driving verbs take their package last, so flags sit between the verb and
// it. survey takes two positional arguments and so must take its flags first,
// and the stdlib flag package stops at the first non-flag argument — meaning
// `sqlb survey $SRC $DST -modules a,b` silently ignores -modules. It says so
// instead.
func TestSurveyRefusesAFlagAfterTheDSNs(t *testing.T) {
	code, out := invoke(t, "survey", "postgres:///src", "postgres:///dst", "-modules", "billing")
	if code == 0 {
		t.Fatalf("a flag after the DSNs was accepted, so -modules would have been ignored:\n%s", out)
	}
	if !strings.Contains(out, "flags go before the two DSNs") {
		t.Errorf("the error did not say where the flag belongs:\n%s", out)
	}
}

// The default exclusion list is narrowed to what a database actually holds
// before it reaches introspect, which reports an Exclude entry it cannot find.
// Without that narrowing every survey would report four absent migration
// runners as skipped constructs — a finding about this command's defaults
// rather than about the schema.
func TestIntersectKeepsOnlyWhatIsPresent(t *testing.T) {
	got := intersect(defaultExcluded, []string{"goose_db_version", "invoices", "users"})
	if len(got) != 1 || got[0] != "goose_db_version" {
		t.Errorf("intersect(defaults, [goose_db_version invoices users]) = %v, want [goose_db_version]", got)
	}
	if got := intersect(defaultExcluded, []string{"invoices"}); len(got) != 0 {
		t.Errorf("a database on no known runner excluded %v, want nothing", got)
	}
}

// Longest prefix wins, so a module named "user" does not claim the tables of
// one named "user_billing" — a wrong split reads as a real result.
func TestByModuleAssignsTheLongestPrefix(t *testing.T) {
	var out strings.Builder
	printByModule(report{&out}, "user,user_billing", []tableResult{
		{Name: "user_billing_invoices"},
		{Name: "user_accounts"},
		{Name: "audit_log", Skips: []introspect.Skip{{Reason: "no"}}},
	})
	got := out.String()
	for _, want := range []string{
		"| user | 1 | 1 | 0 | 0 | green |",
		"| user_billing | 1 | 1 | 0 | 0 | green |",
		"| _(no prefix)_ | 1 | 0 | 1 | 0 | **blocked** |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the by-module table is missing %q:\n%s", want, got)
		}
	}
	// The count that decides how much of a port is blocked. audit_log matches
	// no prefix, so it blocks the unclaimed group and neither declared module —
	// three groups on the table, one of them blocked.
	//
	// The numerator and the denominator must count the same population. They
	// did not: the no-prefix row incremented the count and `len(prefixes)` was
	// the total, which on a real survey printed "3 of 3 modules blocked" over a
	// table containing a green row.
	if !strings.Contains(got, "**1 of 3 modules blocked.**") {
		t.Errorf("the blocked count is wrong, and it is what the report is read for:\n%s", got)
	}
}

// A prefix nothing matches must still appear, green: a module with no tables in
// this database is a fact about the survey's arguments, and dropping the row
// would make it look like the prefix was never passed.
func TestByModuleKeepsAModuleWithNoTables(t *testing.T) {
	var out strings.Builder
	printByModule(report{&out}, "billing,catalog", []tableResult{{Name: "billing_invoices"}})
	got := out.String()
	if !strings.Contains(got, "| catalog | 0 | 0 | 0 | 0 | green |") {
		t.Errorf("a module with no tables was dropped from the table:\n%s", got)
	}
}

func TestByModuleSaysNothingWithoutPrefixes(t *testing.T) {
	var out strings.Builder
	printByModule(report{&out}, "  ", []tableResult{{Name: "invoices"}})
	if out.Len() != 0 {
		t.Errorf("the by-module section was printed without -modules:\n%s", out.String())
	}
}

func TestOnelineFlattensAndTruncates(t *testing.T) {
	if got := oneline("a\n  b\tc "); got != "a b c" {
		t.Errorf("oneline collapsed to %q, want %q", got, "a b c")
	}
	long := oneline(strings.Repeat("x", 500))
	if !strings.HasSuffix(long, "…") || len([]rune(long)) != 161 {
		t.Errorf("oneline returned %d runes, want 160 and an ellipsis", len([]rune(long)))
	}
}

func TestKindOfReducesACommentToItsVerb(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ALTER TABLE invoices ADD COLUMN paid", "ALTER TABLE"},
		{"CREATE", "CREATE"},
		{"", "(unlabelled)"},
	} {
		if got := kindOf(tc.in); got != tc.want {
			t.Errorf("kindOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
