package recipes_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/example/recipes"
)

// pgError stands in for the driver's own error type. What matters is the one
// method: a driver error carrying SQLSTATE is how sqlb classifies a refused
// write without importing any driver.
type pgError struct {
	code       string
	constraint string
	message    string
}

func (e *pgError) Error() string    { return e.message }
func (e *pgError) SQLState() string { return e.code }

// The sentinel errors, and what each one means:
//
//	ErrNotFound     One or First matched nothing
//	ErrConstraint   the database refused the write
//	ErrUnscoped     an update or delete with no Where
//	ErrBadCursor    ?cursor= did not decode against this ordering
//	ErrAfterCommit  the transaction committed, but a callback failed
//
// Every one is testable with errors.Is, so a handler branches on the class
// rather than on the text of a message.
func Example_errorsSentinels() {
	_, err := sqlb.Query[recipes.Post]().
		Where(sqlb.F("id").Eq("nope")).
		One(context.Background(), recordingDBWith(postColumns))

	fmt.Println("is ErrNotFound:", errors.Is(err, sqlb.ErrNotFound))
	fmt.Println("message:       ", err)
	// Output:
	// is ErrNotFound: true
	// message:        sqlb: no rows matched
}

// A constraint violation is the caller's mistake far more often than it is an
// outage: a second signup on a taken email, an order naming a product that was
// deleted, a balance a CHECK will not let go negative.
//
// errors.Is is the cheap test for the class. errors.As gets the detail — which
// rule was broken, and which constraint, where the driver reports a name.
func Example_errorsConstraintViolation() {
	db := failingDB(&pgError{code: "23505", message: `duplicate key value violates unique constraint "authors_email_key"`})

	author := recipes.Author{Email: "ada@example.com"}
	_, err := sqlb.InsertRows(&author).One(context.Background(), db)

	fmt.Println("is ErrConstraint:", errors.Is(err, sqlb.ErrConstraint))

	var ce *sqlb.ConstraintError
	if errors.As(err, &ce) {
		fmt.Println("kind:", ce.Kind)
	}
	// Output:
	// is ErrConstraint: true
	// kind: unique
}

// The built-in classification recovers the kind and nothing else, because the
// standard library defines no way to read a constraint *name* from an error and
// every driver exposes it as a struct field rather than as a method. Reaching
// it means naming the driver, which this library will not do — so the seam is
// SetErrorClassifier, called once at startup.
//
// The name is the field that carries the value: it is what lets a handler say
// "that email is taken" rather than "something was already there".
func Example_errorsClassifierFillsInTheName() {
	sqlb.SetErrorClassifier(func(err error) (sqlb.ConstraintError, bool) {
		// In an application: `var pg *pgconn.PgError; errors.As(err, &pg)`.
		var pg *pgError
		if !errors.As(err, &pg) {
			return sqlb.ConstraintError{}, false
		}
		kind, ok := sqlb.ConstraintKindOf(pg.SQLState())
		if !ok {
			return sqlb.ConstraintError{}, false
		}
		return sqlb.ConstraintError{Kind: kind, Constraint: pg.constraint, Table: "authors"}, true
	})
	defer sqlb.SetErrorClassifier(nil) // an application never does this

	db := failingDB(&pgError{
		code:       "23505",
		constraint: "authors_email_key",
		message:    "duplicate key value violates unique constraint",
	})

	author := recipes.Author{Email: "ada@example.com"}
	_, err := sqlb.InsertRows(&author).One(context.Background(), db)

	var ce *sqlb.ConstraintError
	if errors.As(err, &ce) {
		fmt.Printf("%s on %s.%s\n", ce.Kind, ce.Table, ce.Constraint)
		// Which is what a handler branches on:
		if ce.Constraint == "authors_email_key" {
			fmt.Println("response: that email address is already registered")
		}
	}
	// Output:
	// unique on authors.authors_email_key
	// response: that email address is already registered
}

// An error that is not a constraint violation stays what it was. A syntax
// error or a dead connection must never arrive dressed as the caller's fault,
// which is why the classification is a whitelist of SQLSTATE class 23 rather
// than "a write failed, so blame the input".
func Example_errorsOtherFailuresAreNotConstraints() {
	db := failingDB(&pgError{code: "08006", message: "connection failure"})

	author := recipes.Author{Email: "ada@example.com"}
	_, err := sqlb.InsertRows(&author).One(context.Background(), db)

	fmt.Println("is ErrConstraint:", errors.Is(err, sqlb.ErrConstraint))

	// The driver's own error is still reachable, wrapped rather than replaced,
	// so a caller that does depend on its driver loses nothing.
	var pg *pgError
	fmt.Println("driver error reachable:", errors.As(err, &pg), pg.SQLState())
	// Output:
	// is ErrConstraint: false
	// driver error reachable: true 08006
}

// After-commit callbacks run once the transaction has committed, so a failure
// in one cannot roll anything back. A failing callback does not stop the
// others either — these are independent side effects, and abandoning the rest
// leaves more inconsistency rather than less. The failures come back joined
// under ErrAfterCommit.
func Example_errorsAfterCommitFailure() {
	db := recordingDB()

	err := db.WithTx(context.Background(), func(ctx context.Context, _ *sqlb.DB) error {
		if err := sqlb.AfterCommit(ctx, func(context.Context) error {
			return errors.New("the event bus was down")
		}); err != nil {
			return err
		}
		return sqlb.AfterCommit(ctx, func(context.Context) error {
			fmt.Println("the second callback still ran")
			return nil
		})
	})

	fmt.Println("committed:", count(statements(), "COMMIT") == 1)
	fmt.Println("is ErrAfterCommit:", errors.Is(err, sqlb.ErrAfterCommit))
	// Output:
	// the second callback still ran
	// committed: true
	// is ErrAfterCommit: true
}
