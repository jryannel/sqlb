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
	return strings.Join(lines, "\n")
}
