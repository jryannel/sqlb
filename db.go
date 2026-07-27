package sqlb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Beginner is the subset of *sql.DB that opens a transaction. It is asserted
// for rather than required, so Executor stays two methods and every wrapper
// written against it keeps working.
//
// A wrapper that wants WithTx to work through it — a tracer, a pool adapter —
// implements this alongside Executor and returns the underlying *sql.Tx.
type Beginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// DB is a handle carrying an Executor and the hook registry that its queries
// resolve against.
//
// It is itself an Executor, which is what makes it additive: every terminal
// call already takes one, so passing a *DB where a *sql.DB used to go changes
// nothing except which registry the hooks come from.
//
//	db := sqlb.New(pool)
//	posts, err := sqlb.Query[Post]().All(ctx, db)
//
// The reason to want one is WithTx. A unit of work needs every statement in it
// to run on the same connection, and hooks need to be able to tell that they
// are inside one — neither is expressible when the executor is threaded through
// call sites individually and the registry is a process global.
//
// Go 1.27 makes the call syntax nicer without changing this object graph: the
// package-level generic functions gain method forms on *DB, as the README's
// table describes. The handle is what those methods will hang off, which is why
// it is worth building now rather than with the toolchain.
type DB struct {
	exec  Executor
	hooks *Registry
	// inTx records that this handle is already inside a transaction, so a
	// nested WithTx joins it rather than opening a second one.
	inTx bool
}

// New returns a handle over exec, using the process-default hook registry — so
// hooks registered with On[T]() apply to it, and an existing program can adopt
// the handle without moving its registrations.
func New(exec Executor) *DB {
	if exec == nil {
		panic("sqlb: New called with a nil Executor")
	}
	return &DB{exec: exec, hooks: defaultRegistry}
}

// WithHooks returns a copy of the handle resolving hooks against r instead of
// the process default. It is how a test gets isolation without Reset, and how
// two tenants-worth of differing domain rules can coexist in one process.
func (d *DB) WithHooks(r *Registry) *DB {
	if r == nil {
		panic("sqlb: WithHooks called with a nil Registry")
	}
	clone := *d
	clone.hooks = r
	return &clone
}

// Hooks returns the registry this handle resolves against.
func (d *DB) Hooks() *Registry { return d.hooks }

// InTx reports whether this handle is inside a transaction. A BeforeQuery hook
// that must not run its own statements outside the caller's unit of work can
// check it.
func (d *DB) InTx() bool { return d.inTx }

// QueryContext satisfies Executor.
func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.exec.QueryContext(ctx, query, args...)
}

// ExecContext satisfies Executor.
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.exec.ExecContext(ctx, query, args...)
}

// WithTx runs fn inside a transaction, committing if it returns nil and rolling
// back otherwise. The handle passed to fn executes on the transaction, so every
// statement in the unit of work lands on one connection:
//
//	err := db.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
//	    order, err := sqlb.InsertRows(&o).One(ctx, tx)
//	    if err != nil {
//	        return err
//	    }
//	    _, err = sqlb.UpdateRows[Stock]().
//	        Set("reserved", true).
//	        Where(sqlb.F("sku").Eq(order.SKU)).
//	        Exec(ctx, tx)
//	    return err
//	})
//
// fn receives a context carrying the transaction, which is what makes TxFrom
// work inside hooks — so pass that ctx onward rather than the enclosing one.
//
// A panic in fn rolls back and is re-raised, so a transaction is never left
// open by one.
//
// Nesting joins rather than nests: calling WithTx on a handle that is already
// in a transaction runs fn on that same transaction and leaves the commit to
// the outermost call. Savepoints would be the alternative and are a larger
// promise — partial rollback changes what "the unit of work succeeded" means,
// and nothing needs it yet. Joining keeps a function that opens a transaction
// callable from inside one.
func (d *DB) WithTx(ctx context.Context, fn func(ctx context.Context, tx *DB) error) error {
	return d.WithTxOptions(ctx, nil, fn)
}

// WithTxOptions is WithTx with an explicit isolation level or read-only flag.
//
// The options are ignored when joining an outer transaction, since isolation is
// a property of the transaction and the outer one has already begun. Asking for
// stricter isolation than the enclosing transaction provides is therefore an
// error rather than a silent downgrade.
func (d *DB) WithTxOptions(ctx context.Context, opts *sql.TxOptions, fn func(ctx context.Context, tx *DB) error) error {
	if fn == nil {
		return errors.New("sqlb: WithTx called with a nil function")
	}
	if d.inTx {
		if err := compatibleWithOuter(opts); err != nil {
			return err
		}
		return fn(ctx, d)
	}

	beginner, ok := d.exec.(Beginner)
	if !ok {
		return fmt.Errorf("sqlb: WithTx needs an executor that can begin a transaction, "+
			"but %T only implements Executor; pass the *sql.DB itself, or implement "+
			"BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error) on the wrapper", d.exec)
	}
	sqlTx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("sqlb: beginning transaction: %w", err)
	}

	tx := &DB{exec: sqlTx, hooks: d.hooks, inTx: true}
	txCtx := context.WithValue(ctx, txKey{}, tx)

	// A panic must not leave the transaction open, and it must still reach the
	// caller: rollback, then re-raise with the original stack.
	committed := false
	defer func() {
		if committed {
			return
		}
		if p := recover(); p != nil {
			_ = sqlTx.Rollback()
			panic(p)
		}
	}()

	if err := fn(txCtx, tx); err != nil {
		if rbErr := sqlTx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			// Both matter: the caller's error says why the unit of work
			// failed, the rollback error says the connection may be unusable.
			return errors.Join(err, fmt.Errorf("sqlb: rolling back: %w", rbErr))
		}
		return err
	}

	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("sqlb: committing transaction: %w", err)
	}
	committed = true
	return nil
}

// compatibleWithOuter rejects options that the outer transaction cannot honour.
// Silently ignoring them would give a caller that asked for Serializable a
// weaker guarantee than it believes it has.
func compatibleWithOuter(opts *sql.TxOptions) error {
	if opts == nil {
		return nil
	}
	if opts.Isolation != sql.LevelDefault {
		return fmt.Errorf("sqlb: WithTxOptions asked for %s inside an existing transaction, "+
			"whose isolation is already fixed; request it on the outermost WithTx instead",
			opts.Isolation)
	}
	// ReadOnly is a narrowing, but Postgres will not accept it mid-transaction
	// either, and pretending it applied would be worse than saying so.
	if opts.ReadOnly {
		return errors.New("sqlb: WithTxOptions asked for a read-only transaction inside an " +
			"existing one, which cannot be narrowed after it has begun; request it on the " +
			"outermost WithTx instead")
	}
	return nil
}

// txKey addresses the transaction handle in a context.
type txKey struct{}

// TxFrom returns the transaction handle a hook is running under, if any.
//
// This is what lets a hook take part in the unit of work it was triggered by.
// A BeforeQuery that needs to see rows written earlier in the same transaction
// must read through this handle — reading through the process-wide pool would
// miss them, because they are not committed yet:
//
//	sqlb.On[Post]().BeforeCreate(func(ctx context.Context, p *Post) error {
//	    tx, ok := sqlb.TxFrom(ctx)
//	    if !ok {
//	        return errors.New("posts must be created inside a transaction")
//	    }
//	    n, err := sqlb.Query[Post]().Where(sqlb.F("slug").Eq(p.Slug)).Count(ctx, tx)
//	    ...
//	})
func TxFrom(ctx context.Context) (*DB, bool) {
	tx, ok := ctx.Value(txKey{}).(*DB)
	return tx, ok
}

// hooksFor resolves the hook set for T against whichever registry the executor
// carries, falling back to the process default. It is the one place that knows
// hooks can be scoped, so terminal methods keep taking a plain Executor.
func hooksFor[T any](exec Executor) *Hooks[T] {
	if d, ok := exec.(*DB); ok {
		return OnIn[T](d.hooks)
	}
	return On[T]()
}
