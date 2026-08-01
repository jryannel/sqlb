package introspect

import (
	"fmt"
	"sort"
	"strings"
)

// Report collects everything in the database that the DSL cannot express.
//
// It exists because the dangerous failure here is a quiet one. A schema missing
// a construct still compiles, still validates, and still produces a migration —
// one that proposes undoing whatever it failed to see. ADR-0014 names silently
// dropping as the failure mode to watch for hardest, so nothing is dropped
// without an entry here.
//
// An empty Report means the registry describes the database completely. A
// non-empty one means it does not, and the difference is yours to reconcile.
type Report struct {
	Skipped []Skip

	// Notes records a construct that survived, but not in the shape the
	// database holds it — a decision this package made rather than a gap it
	// left. A foreign key on an import-breaking cycle is the case: it is
	// imported as the enforced ExternalRef a declaration would also be forced
	// to write, which is faithful, but the reader should know which side was
	// chosen.
	//
	// Deliberately not Skipped. Empty and Err are about whether the registry
	// describes the database, and a note does not change that answer — putting
	// one there would fail a round-trip that is in fact clean.
	Notes []string
}

// Skip is one construct that did not survive the import, where it was, and what
// to do about it.
type Skip struct {
	Table  string
	Object string // the constraint, index or column it concerns
	Reason string
	// Def is the definition Postgres reports, so the construct can be carried
	// across by hand without going back to the database to look it up.
	Def string
}

func (r *Report) add(table, object, reason, def string) {
	r.Skipped = append(r.Skipped, Skip{Table: table, Object: object, Reason: reason, Def: def})
}

func (r *Report) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// Empty reports whether everything in the database was represented.
func (r *Report) Empty() bool { return r == nil || len(r.Skipped) == 0 }

// Err returns an error describing everything skipped, or nil if nothing was.
//
// It is offered rather than returned from Registry because whether an
// unrepresentable construct is fatal depends on what you are doing. Adopting a
// database wants to see the list and carry the remainder over by hand;
// round-tripping a schema this package generated wants any entry at all to be a
// failure.
func (r *Report) Err() error {
	if r.Empty() {
		return nil
	}
	return fmt.Errorf("introspect: %d construct(s) could not be represented:\n%s",
		len(r.Skipped), r)
}

func (r *Report) String() string {
	if r.Empty() {
		if len(r.Notes) > 0 {
			return "introspect: everything represented\n" + r.notes()
		}
		return "introspect: everything represented"
	}
	lines := make([]string, 0, len(r.Skipped))
	for _, s := range r.Skipped {
		where := s.Table
		if s.Object != "" {
			where += "." + s.Object
		}
		line := "  " + where + ": " + s.Reason
		if s.Def != "" {
			line += "\n      " + s.Def
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	out := strings.Join(lines, "\n")
	if len(r.Notes) > 0 {
		out += "\n" + r.notes()
	}
	return out
}

// notes renders the decisions this package made, under a heading that keeps
// them apart from the gaps above: a gap is something to reconcile, a note is
// something to know.
func (r *Report) notes() string {
	lines := make([]string, 0, len(r.Notes))
	for _, n := range r.Notes {
		lines = append(lines, "  "+n)
	}
	sort.Strings(lines)
	return "imported in a different shape:\n" + strings.Join(lines, "\n")
}
