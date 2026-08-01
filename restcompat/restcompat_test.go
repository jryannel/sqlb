package restcompat_test

import (
	"strings"
	"testing"

	"github.com/jryannel/sqlb/restcompat"
	"github.com/jryannel/sqlb/schema"
)

// opts parametrises the blog's posts table so a test can state a before and an
// after that differ in exactly one way. The zero value is the baseline blog
// contract; each field turns on one edit.
type opts struct {
	titleName         string    // rename target for title; "" keeps "title"
	titleNullable     bool      // NOT NULL -> nullable on title (reader break)
	statusUnfilter    bool      // drop Filterable from status (un-expose, no DDL)
	dropViewCount     bool      // drop a column (destructive migration)
	publishedNotNull  bool      // nullable -> NOT NULL on published_at (writer break)
	addSubtitle       bool      // add a nullable filterable column (additive)
	addRequiredSlug   bool      // add a NOT NULL no-default column (writer break)
	widenViewCount    bool      // bigint stays; flip to int to test narrowing
	titleReadOnly     bool      // writable -> ReadOnly on title (leaves both bodies)
	titleImmutable    bool      // writable -> Immutable on title (leaves the patch body)
	viewCountWritable bool      // ReadOnly -> writable on view_count
	statusValues      []string  // enum values; nil keeps the baseline three
	ops               schema.Op // 0 keeps the baseline op set
}

const baseOps = schema.OpCreate | schema.OpRead | schema.OpUpdate | schema.OpList

// blog builds a registry holding the blog's posts table (and an unexposed
// authors table for the reference to point at), edited per o.
func blog(o opts) *schema.Registry {
	r := schema.NewRegistry()

	authors := r.Table("authors", schema.UUIDv7("id").PrimaryKey())

	title := schema.Text(pick(o.titleName, "title")).Searchable().Sortable()
	if o.titleName != "" {
		title = title.RenamedFrom("title")
	}
	if o.titleNullable {
		title = title.Nullable()
	}
	if o.titleReadOnly {
		title = title.ReadOnly()
	}
	if o.titleImmutable {
		title = title.Immutable()
	}

	status := schema.Enum("status", pickVals(o.statusValues, "draft", "review", "published")...).
		Default(schema.Value("draft")).Sortable()
	if !o.statusUnfilter {
		status = status.Filterable()
	}

	viewType := schema.BigInt
	if o.widenViewCount {
		viewType = schema.Int
	}

	published := schema.Timestamp("published_at").Filterable().Sortable()
	if !o.publishedNotNull {
		published = published.Nullable()
	}

	fields := []schema.FieldSpec{
		schema.UUIDv7("id").PrimaryKey(),
		schema.Ref("author", authors).Filterable().Expandable().Inverse("posts"),
		title,
		schema.Text("body").Searchable(),
		status,
		published,
	}
	if !o.dropViewCount {
		views := viewType("view_count").Filterable().Sortable()
		if !o.viewCountWritable {
			views = views.Default(schema.Value(0)).ReadOnly()
		}
		// When writable it also loses its default, so it arrives *required* —
		// the half of "no longer read-only" that breaks a client rather than
		// the half that does not.
		fields = append(fields, views)
	}
	if o.addSubtitle {
		fields = append(fields, schema.Text("subtitle").Nullable().Filterable())
	}
	if o.addRequiredSlug {
		fields = append(fields, schema.Text("slug"))
	}
	fields = append(fields, schema.Timestamps())

	r.Table("posts", fields...).
		Expose(schema.REST{Path: "/posts", Ops: pickOps(o.ops, baseOps)})
	return r
}

func TestNoChangeIsEmpty(t *testing.T) {
	if got := restcompat.Diff(blog(opts{}), blog(opts{})); len(got) != 0 {
		t.Fatalf("identical schemas should diff empty, got:\n%s", render(got))
	}
}

// A rename is a clean migration and a hard wire break — the case that proves the
// API check is not a by-product of the migration check (ADR-0039).
func TestRenameIsAWireBreak(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}), blog(opts{titleName: "headline"}))
	assertBreaking(t, breaks, restcompat.FacetResponse, "headline")
	assertBreaking(t, breaks, restcompat.FacetFilter, "headline")
	if !mentions(breaks, "renamed from") {
		t.Errorf("rename should be reported as a rename, not a drop and add:\n%s", render(breaks))
	}
	assertNoAdditive(t, breaks) // it is a rename, not a new field
}

// Un-exposing a filter changes the contract while emitting no DDL at all — the
// break a migration-shaped check cannot see.
func TestUnExposeFilterBreaksWithNoDDL(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}), blog(opts{statusUnfilter: true}))
	assertBreaking(t, breaks, restcompat.FacetFilter, "status")
	// status stays sortable and in responses, so nothing else should fire.
	if n := len(restcompat.Breaking(breaks)); n != 1 {
		t.Errorf("want exactly one breaking change, got %d:\n%s", n, render(breaks))
	}
}

func TestDropOperationBreaks(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}), blog(opts{ops: baseOps &^ schema.OpRead}))
	assertBreaking(t, breaks, restcompat.FacetOps, "")
	if !mentions(breaks, "operation read removed") {
		t.Errorf("want the removed read operation named:\n%s", render(breaks))
	}
}

func TestDropColumnBreaksEveryCapability(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}), blog(opts{dropViewCount: true}))
	assertBreaking(t, breaks, restcompat.FacetResponse, "view_count")
	assertBreaking(t, breaks, restcompat.FacetFilter, "view_count")
	assertBreaking(t, breaks, restcompat.FacetSort, "view_count")
}

// The additive baseline: a nullable, filterable column breaks nobody.
func TestAddNullableFilterableIsAdditive(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}), blog(opts{addSubtitle: true}))
	if got := restcompat.Breaking(breaks); len(got) != 0 {
		t.Fatalf("adding a nullable filterable column should break nobody, got:\n%s", render(got))
	}
	assertLevel(t, breaks, restcompat.LevelAdditive, restcompat.FacetFilter, "subtitle")
	assertLevel(t, breaks, restcompat.LevelAdditive, restcompat.FacetResponse, "subtitle")
}

// nullable -> NOT NULL: neutral for readers, breaking for writers. The both-ways
// case ADR-0016 says must be reported on both sides, never folded.
func TestNullableToNotNullSplitsReaderAndWriter(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}), blog(opts{publishedNotNull: true}))
	assertLevel(t, breaks, restcompat.LevelNeutral, restcompat.FacetResponse, "published_at")
	assertLevel(t, breaks, restcompat.LevelBreaking, restcompat.FacetCreate, "published_at")
}

// not null -> nullable: breaking for readers, additive for writers.
func TestNotNullToNullableBreaksReaders(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}), blog(opts{titleNullable: true}))
	assertLevel(t, breaks, restcompat.LevelBreaking, restcompat.FacetResponse, "title")
	assertLevel(t, breaks, restcompat.LevelAdditive, restcompat.FacetCreate, "title")
}

func TestNewRequiredFieldBreaksCreate(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}), blog(opts{addRequiredSlug: true}))
	assertBreaking(t, breaks, restcompat.FacetCreate, "slug")
}

// The writer side of the contract, which the snapshot captured and the diff
// never read: three breaks that let `sqlb impact -error` pass CI on a breaking
// deploy (#68). The reader side of diffField was thorough throughout, and no
// existing test touched a body-only capability, which is what let it ship.

// writable -> ReadOnly: the column leaves both generated bodies, so a client
// that sends it now 422s with "unknown field".
func TestBecomingReadOnlyBreaksBothBodies(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}), blog(opts{titleReadOnly: true}))
	assertLevel(t, breaks, restcompat.LevelBreaking, restcompat.FacetCreate, "title")
	assertLevel(t, breaks, restcompat.LevelBreaking, restcompat.FacetPatch, "title")
	if !mentions(breaks, "422") {
		t.Errorf("the summary should name the client-visible consequence:\n%s", render(breaks))
	}
}

// writable -> Immutable: it leaves the PATCH body only. Create is unaffected,
// which is the whole distinction between Immutable and ReadOnly.
func TestBecomingImmutableBreaksThePatchBodyOnly(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}), blog(opts{titleImmutable: true}))
	assertLevel(t, breaks, restcompat.LevelBreaking, restcompat.FacetPatch, "title")
	for _, b := range breaks {
		if b.Facet == restcompat.FacetCreate {
			t.Errorf("Immutable must not touch the create body:\n%s", render(breaks))
		}
	}
}

// ReadOnly -> writable on a NOT NULL, no-default column: it becomes *required*
// at create, so a client that omitted it — which is every client, since it could
// not send it before — now fails validation.
func TestLeavingReadOnlyWithoutADefaultBreaksCreate(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}), blog(opts{viewCountWritable: true}))
	assertLevel(t, breaks, restcompat.LevelBreaking, restcompat.FacetCreate, "view_count")
	if !mentions(breaks, "required") {
		t.Errorf("the summary should say why it breaks:\n%s", render(breaks))
	}
	// And the patch body gained it, which breaks nobody.
	assertLevel(t, breaks, restcompat.LevelAdditive, restcompat.FacetPatch, "view_count")
}

// Widening an integer is not claimed neutral: a narrow generated client can
// overflow, so it surfaces as unknown (which a strict gate treats as breaking).
func TestWidenIntegerIsUnknownNotNeutral(t *testing.T) {
	// old = int, new = bigint (widen). blog()'s baseline is bigint, so flip the
	// direction: old widens view_count to int, new keeps bigint.
	breaks := restcompat.Diff(blog(opts{widenViewCount: true}), blog(opts{}))
	assertLevel(t, breaks, restcompat.LevelUnknown, restcompat.FacetResponse, "view_count")
	if len(restcompat.Breaking(breaks)) == 0 {
		t.Errorf("an unknown type change should count under a strict gate:\n%s", render(breaks))
	}
}

func TestEnumGainedValueBreaksReaders(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}),
		blog(opts{statusValues: []string{"draft", "review", "published", "archived"}}))
	assertBreaking(t, breaks, restcompat.FacetResponse, "status")
	if !mentions(breaks, "archived") {
		t.Errorf("the new enum value should be named:\n%s", render(breaks))
	}
}

func TestEnumDroppedValueBreaksInput(t *testing.T) {
	breaks := restcompat.Diff(blog(opts{}),
		blog(opts{statusValues: []string{"draft", "published"}}))
	assertBreaking(t, breaks, restcompat.FacetFilter, "status")
	if !mentions(breaks, "review") {
		t.Errorf("the dropped enum value should be named:\n%s", render(breaks))
	}
}

// --- assertions ---------------------------------------------------------------

func assertBreaking(t *testing.T, breaks []restcompat.Break, facet restcompat.Facet, field string) {
	t.Helper()
	assertLevel(t, breaks, restcompat.LevelBreaking, facet, field)
}

func assertLevel(t *testing.T, breaks []restcompat.Break, lvl restcompat.Level, facet restcompat.Facet, field string) {
	t.Helper()
	for _, b := range breaks {
		if b.Level == lvl && b.Facet == facet && b.Field == field {
			return
		}
	}
	t.Errorf("missing %s break on %s %q, got:\n%s", lvl, facet, field, render(breaks))
}

func assertNoAdditive(t *testing.T, breaks []restcompat.Break) {
	t.Helper()
	for _, b := range breaks {
		if b.Level == restcompat.LevelAdditive {
			t.Errorf("unexpected additive break:\n%s", render(breaks))
			return
		}
	}
}

func mentions(breaks []restcompat.Break, substr string) bool {
	for _, b := range breaks {
		if strings.Contains(b.Summary, substr) {
			return true
		}
	}
	return false
}

func render(breaks []restcompat.Break) string {
	var b strings.Builder
	for _, br := range breaks {
		b.WriteString("  " + br.String() + "\n")
	}
	if b.Len() == 0 {
		return "  (none)"
	}
	return b.String()
}

func pick(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

func pickVals(v []string, dflt ...string) []string {
	if v == nil {
		return dflt
	}
	return v
}

func pickOps(o, dflt schema.Op) schema.Op {
	if o == 0 {
		return dflt
	}
	return o
}
