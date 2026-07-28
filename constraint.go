package sqlb

import (
	"errors"
	"fmt"
	"sync/atomic"
)

// ErrConstraint is the class of every write a database constraint refused.
//
// It exists so that a caller can tell its own mistake from the library's
// without depending on a driver:
//
//	if errors.Is(err, sqlb.ErrConstraint) { … }
//
// A caller needing to know *which* constraint matches a *ConstraintError with
// errors.As instead.
var ErrConstraint = errors.New("sqlb: constraint violated")

// ConstraintKind names the integrity rule a write broke. It is SQLSTATE class
// 23, in the terms a schema declares rather than in the terms Postgres numbers
// them, so that a caller switching on it reads as the schema does.
type ConstraintKind string

const (
	// ConstraintUnique is a duplicate value in a unique index (23505).
	ConstraintUnique ConstraintKind = "unique"
	// ConstraintForeignKey is a reference to a row that is not there, or a
	// delete of a row still referenced (23503).
	ConstraintForeignKey ConstraintKind = "foreign_key"
	// ConstraintCheck is a CHECK expression that evaluated false (23514).
	ConstraintCheck ConstraintKind = "check"
	// ConstraintNotNull is a NULL in a column declared NOT NULL (23502).
	ConstraintNotNull ConstraintKind = "not_null"
	// ConstraintExclusion is an EXCLUDE constraint (23P01).
	ConstraintExclusion ConstraintKind = "exclusion"
)

// kindOfSQLState maps SQLSTATE class 23 onto a ConstraintKind. Codes outside
// the class are not constraint violations and report false, so a syntax error
// or a dead connection never arrives dressed as the caller's fault.
func kindOfSQLState(code string) (ConstraintKind, bool) {
	switch code {
	case "23505":
		return ConstraintUnique, true
	case "23503":
		return ConstraintForeignKey, true
	case "23514":
		return ConstraintCheck, true
	case "23502":
		return ConstraintNotNull, true
	case "23P01":
		return ConstraintExclusion, true
	default:
		return "", false
	}
}

// ConstraintError reports a write the database refused because it would have
// broken a constraint.
//
// This is the caller's mistake far more often than it is an outage: a second
// signup on a taken email, an order naming a product that was deleted, a
// balance a CHECK will not let go negative. Without it those arrive as an
// opaque driver error, and the only way to tell them apart is to match on the
// text of a message — which no rename survives, and which every application
// with a unique index otherwise ends up writing.
//
// Kind is always set. The remaining fields are filled only as far as the
// driver reports them: the standard library defines no way to read a
// constraint name from an error, so the built-in classification recovers the
// kind alone. Registering a driver-aware SetErrorClassifier fills in the rest.
type ConstraintError struct {
	// Kind is the integrity rule that was broken.
	Kind ConstraintKind
	// Constraint is the name of the index or constraint that refused the
	// write, where the driver reports one. It is the name the schema declares,
	// so a caller can match on it rather than on prose.
	Constraint string
	// Table is the relation the constraint belongs to, where reported.
	Table string
	// Column is the column at fault, where the constraint names exactly one —
	// which for a NOT NULL violation it does, and for a composite unique index
	// it does not.
	Column string
	// Detail is the driver's own elaboration, where it offers one. It can name
	// the conflicting values, so it is a developer-facing string rather than
	// something to put in a response.
	Detail string

	err error
}

// Error implements error. The wrapped driver error is included, so a log line
// carries what the database actually said.
func (e *ConstraintError) Error() string {
	var subject string
	switch {
	case e.Constraint != "":
		subject = fmt.Sprintf("%s constraint %q", e.Kind, e.Constraint)
	default:
		subject = fmt.Sprintf("%s constraint", e.Kind)
	}
	if e.err == nil {
		return "sqlb: " + subject + " violated"
	}
	return fmt.Sprintf("sqlb: %s violated: %v", subject, e.err)
}

// Unwrap returns the driver's error, so a caller that does depend on its
// driver can still reach the original.
func (e *ConstraintError) Unwrap() error { return e.err }

// Is reports ErrConstraint, making errors.Is the cheap test for the class.
func (e *ConstraintError) Is(target error) bool { return target == ErrConstraint }

// ErrorClassifier turns a driver's error into a ConstraintError. It reports
// false for anything that is not a constraint violation.
type ErrorClassifier func(error) (ConstraintError, bool)

var classifier atomic.Pointer[ErrorClassifier]

// SetErrorClassifier installs a driver-aware classifier, which supersedes the
// built-in one.
//
// The built-in classification is deliberately dependency-free: it reads
// SQLSTATE through an interface a driver error may satisfy, which recovers the
// kind and nothing else. The constraint *name* is the field carrying the value
// — it is what lets an application branch on which rule was broken — and every
// driver exposes it as a struct field rather than as a method, so reaching it
// means naming the driver. This library depends on the standard library alone
// and will not do that, so the seam is here instead:
//
//	sqlb.SetErrorClassifier(func(err error) (sqlb.ConstraintError, bool) {
//	        var pg *pgconn.PgError
//	        if !errors.As(err, &pg) {
//	                return sqlb.ConstraintError{}, false
//	        }
//	        kind, ok := sqlb.ConstraintKindOf(pg.SQLState())
//	        if !ok {
//	                return sqlb.ConstraintError{}, false
//	        }
//	        return sqlb.ConstraintError{
//	                Kind:       kind,
//	                Constraint: pg.ConstraintName,
//	                Table:      pg.TableName,
//	                Column:     pg.ColumnName,
//	                Detail:     pg.Detail,
//	        }, true
//	})
//
// Call it once at startup, before serving. Passing nil restores the built-in
// classification.
func SetErrorClassifier(fn ErrorClassifier) {
	if fn == nil {
		classifier.Store(nil)
		return
	}
	classifier.Store(&fn)
}

// ConstraintKindOf maps a SQLSTATE code onto a ConstraintKind, reporting false
// for codes outside class 23. It is exported for classifiers, which need the
// same mapping and should not have to restate it.
func ConstraintKindOf(sqlstate string) (ConstraintKind, bool) {
	return kindOfSQLState(sqlstate)
}

// sqlStater is the interface a driver error satisfies when it carries a
// SQLSTATE code. pgx's *pgconn.PgError does, as a method rather than a field,
// which is the whole reason the class is reachable without importing it.
type sqlStater interface{ SQLState() string }

// classifyConstraint reports whether err is a constraint violation, using the
// registered classifier if there is one and SQLSTATE otherwise.
//
// The returned error does not wrap err; callers set the wrapped error to
// whichever annotated form they want a log to show.
func classifyConstraint(err error) (*ConstraintError, bool) {
	if err == nil {
		return nil, false
	}
	if fn := classifier.Load(); fn != nil {
		if ce, ok := (*fn)(err); ok {
			return &ce, true
		}
		// A registered classifier that declines is not a veto: it may know one
		// driver and be handed an error from another, so the built-in check
		// still runs below.
	}
	var s sqlStater
	if !errors.As(err, &s) {
		return nil, false
	}
	kind, ok := kindOfSQLState(s.SQLState())
	if !ok {
		return nil, false
	}
	return &ConstraintError{Kind: kind}, true
}

// asConstraintErr classifies err without re-annotating it. It is for the paths
// where a driver reports a rejected write at scan time rather than at exec
// time, which pgx does for anything on the extended protocol — without this,
// whether an insert's constraint violation is classified would depend on which
// driver was underneath.
func asConstraintErr(err error) error {
	ce, ok := classifyConstraint(err)
	if !ok {
		return err
	}
	ce.err = err
	return ce
}
