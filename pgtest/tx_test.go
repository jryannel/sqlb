package pgtest

import (
	"context"
	"errors"
	"testing"

	"github.com/jryannel/sqlb"
)

// Account is a deliberately small model. What is under test is the transaction
// boundary, not the builder.
type Account struct {
	ID      int64  `db:"id" sqlb:"pk,default"`
	Owner   string `db:"owner" sqlb:"filter"`
	Balance int64  `db:"balance" sqlb:"filter,sort"`
}

func (Account) TableName() string { return "accounts" }

func accountsDB(t *testing.T) *sqlb.DB {
	t.Helper()
	db := freshStockDB(t)
	mustExec(t, db, `
		CREATE TABLE accounts (
			id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			owner   text   NOT NULL,
			balance bigint NOT NULL DEFAULT 0
		)
	`)
	return sqlb.New(db)
}

func countAccounts(t *testing.T, db *sqlb.DB) int64 {
	t.Helper()
	n, err := sqlb.Query[Account]().Count(context.Background(), db)
	if err != nil {
		t.Fatalf("counting accounts: %v", err)
	}
	return n
}

// The fake driver in the root package proves sqlb issues BEGIN and ROLLBACK in
// the right order. Only a real Postgres proves the rollback actually discards
// the rows — that the unit of work is atomic rather than merely well-narrated.
func TestWithTxRollbackDiscardsWrites(t *testing.T) {
	ctx := context.Background()
	db := accountsDB(t)

	sentinel := errors.New("second leg failed")
	err := db.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
		a := Account{Owner: "ada", Balance: 100}
		if _, err := sqlb.InsertRows(&a).One(ctx, tx); err != nil {
			return err
		}
		// Visible inside the transaction, before it is decided.
		if n := countAccounts(t, tx); n != 1 {
			t.Errorf("inside the transaction: %d accounts, want 1", n)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the caller's error", err)
	}

	if n := countAccounts(t, db); n != 0 {
		t.Errorf("after rollback: %d accounts, want 0", n)
	}
}

func TestWithTxCommitPersistsWrites(t *testing.T) {
	ctx := context.Background()
	db := accountsDB(t)

	err := db.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
		for _, owner := range []string{"ada", "grace"} {
			a := Account{Owner: owner, Balance: 10}
			if _, err := sqlb.InsertRows(&a).One(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if n := countAccounts(t, db); n != 2 {
		t.Errorf("after commit: %d accounts, want 2", n)
	}
}

// Two statements landing on one connection is the property the handle exists
// for. Without it the second statement can take a different pooled connection,
// and a constraint the first depends on has not been applied yet.
func TestWithTxKeepsBothLegsOnOneConnection(t *testing.T) {
	ctx := context.Background()
	db := accountsDB(t)

	err := db.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
		a := Account{Owner: "ada", Balance: 100}
		if _, err := sqlb.InsertRows(&a).One(ctx, tx); err != nil {
			return err
		}
		// A LOCAL setting is scoped to the transaction, so reading it back
		// non-zero proves the second statement is on the same one.
		if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '31s'`); err != nil {
			return err
		}
		var timeout string
		rows, err := tx.QueryContext(ctx, `SHOW statement_timeout`)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return errors.New("SHOW returned no rows")
		}
		if err := rows.Scan(&timeout); err != nil {
			return err
		}
		if timeout != "31s" {
			t.Errorf("statement_timeout = %q inside the transaction, want 31s — "+
				"the statements did not share a connection", timeout)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
}

// A hook that must read what the same unit of work has already written can only
// do so through the transaction handle. This is the case the process-global
// registry could not express, because a hook had no way to learn it was inside
// a transaction at all.
func TestHookReadsUncommittedRowsThroughTxFrom(t *testing.T) {
	ctx := context.Background()
	db := accountsDB(t)

	scoped := sqlb.NewRegistry()
	var seenByHook int64 = -1
	sqlb.OnIn[Account](scoped).BeforeCreate(func(ctx context.Context, _ *Account) error {
		tx, ok := sqlb.TxFrom(ctx)
		if !ok {
			return errors.New("accounts must be created inside a transaction")
		}
		n, err := sqlb.Query[Account]().Count(ctx, tx)
		if err != nil {
			return err
		}
		seenByHook = n
		return nil
	})

	err := db.WithHooks(scoped).WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
		first := Account{Owner: "ada", Balance: 1}
		if _, err := sqlb.InsertRows(&first).One(ctx, tx); err != nil {
			return err
		}
		second := Account{Owner: "grace", Balance: 2}
		_, err := sqlb.InsertRows(&second).One(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	// The hook ran before the second insert and saw the first one, which is
	// only possible from inside the transaction.
	if seenByHook != 1 {
		t.Errorf("hook counted %d rows before the second insert, want 1", seenByHook)
	}
}

// Refusing the create rolls the whole unit of work back, including the rows
// that had already succeeded. This is what makes BeforeCreate usable as a
// guard rather than as advice.
func TestHookErrorRollsBackTheUnitOfWork(t *testing.T) {
	ctx := context.Background()
	db := accountsDB(t)

	scoped := sqlb.NewRegistry()
	refuse := errors.New("balance must be positive")
	sqlb.OnIn[Account](scoped).BeforeCreate(func(_ context.Context, a *Account) error {
		if a.Balance <= 0 {
			return refuse
		}
		return nil
	})

	err := db.WithHooks(scoped).WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
		good := Account{Owner: "ada", Balance: 100}
		if _, err := sqlb.InsertRows(&good).One(ctx, tx); err != nil {
			return err
		}
		bad := Account{Owner: "grace", Balance: 0}
		_, err := sqlb.InsertRows(&bad).One(ctx, tx)
		return err
	})
	if !errors.Is(err, refuse) {
		t.Fatalf("error = %v, want the hook's refusal", err)
	}
	if n := countAccounts(t, db); n != 0 {
		t.Errorf("after the hook refused: %d accounts, want 0 — the first insert should have rolled back too", n)
	}
}
