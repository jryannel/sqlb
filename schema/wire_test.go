package schema_test

import (
	"strings"
	"testing"

	"github.com/jryannel/sqlb/schema"
)

// The transformation and its inverse, over the names a real schema has.
func TestWireCaseRoundTrip(t *testing.T) {
	cases := []struct{ column, wire string }{
		{"created_at", "createdAt"},
		{"id", "id"},
		{"org_id", "orgId"},
		{"pos_x", "posX"},
		{"contracted_hours_per_week", "contractedHoursPerWeek"},
		// A leading underscore is structural, not a separator: dropping it
		// would collide _internal and internal onto one wire name.
		{"_internal", "_internal"},
	}
	for _, c := range cases {
		if got := schema.Camel.WireName(c.column); got != c.wire {
			t.Errorf("WireName(%q) = %q, want %q", c.column, got, c.wire)
		}
		if got := schema.Camel.ColumnName(c.wire); got != c.column {
			t.Errorf("ColumnName(%q) = %q, want %q", c.wire, got, c.column)
		}
	}
}

// Verbatim is the identity function in both directions, which is what makes it
// a safe default rather than a special case every caller has to remember.
func TestVerbatimIsIdentity(t *testing.T) {
	for _, n := range []string{"created_at", "posX", "_x", "pos_x_2"} {
		if got := schema.Verbatim.WireName(n); got != n {
			t.Errorf("WireName(%q) = %q", n, got)
		}
		if got := schema.Verbatim.ColumnName(n); got != n {
			t.Errorf("ColumnName(%q) = %q", n, got)
		}
	}
}

// The failure the amendment names: a digit boundary does not survive, so the
// schema is refused at build time rather than shipping a client that asks for a
// column no table has.
func TestWireCaseRefusesNamesItCannotRecover(t *testing.T) {
	r := schema.NewRegistry().WireCase(schema.Camel)
	r.Table("events",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Int("pos_x_2"),
	)
	err := r.Validate()
	if err == nil {
		t.Fatal("pos_x_2 does not round trip and must be refused")
	}
	for _, want := range []string{"pos_x_2", "posX2", "pos_x2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q, so it cannot be acted on:\n%v", want, err)
		}
	}
	// And it says what to do about it.
	if !strings.Contains(err.Error(), "Verbatim") {
		t.Errorf("the error does not offer the way out:\n%v", err)
	}
}

// Two columns landing on one wire name is the other half of the same failure.
func TestWireCaseRefusesACollision(t *testing.T) {
	r := schema.NewRegistry().WireCase(schema.Camel)
	r.Table("t",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("posX"),
		schema.Text("pos_x"),
	)
	if err := r.Validate(); err == nil {
		t.Fatal("two columns spelled the same on the wire must be refused")
	}
}

// A schema whose names all survive validates, which is the case that has to
// keep working for the feature to be usable at all.
func TestWireCaseAcceptsOrdinaryNames(t *testing.T) {
	r := schema.NewRegistry().WireCase(schema.Camel)
	r.Table("members",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("display_name"),
		schema.Timestamp("created_at"),
	)
	if err := r.Validate(); err != nil {
		t.Fatalf("ordinary snake_case must survive: %v", err)
	}
	if r.Wire() != schema.Camel {
		t.Errorf("Wire() = %q", r.Wire())
	}
}

// Verbatim runs no check at all, so a name that camel could not recover is
// simply a column name — which is what every schema written before this is.
func TestVerbatimAcceptsAnything(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("events", schema.UUIDv7("id").PrimaryKey(), schema.Int("pos_x_2"))
	if err := r.Validate(); err != nil {
		t.Fatalf("Verbatim must not police column names: %v", err)
	}
}
