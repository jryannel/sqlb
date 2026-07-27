package sqlb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
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
	// tx is the shared state of the transaction this handle runs in, nil
	// outside one. A nested WithTx hands back the same handle, so callbacks
	// registered by an inner block accumulate here and drain once.
	tx *txState
}

// txState is what a transaction accumulates besides its statements.
type txState struct {
	mu          sync.Mutex
	afterCommit []func(context.Context) error
	drained     bool
}

// ErrAfterCommit reports that the transaction committed but an after-commit
// callback failed. The distinction matters: the write is durable and must not
// be retried, while the side effect did not happen and may need to be.
//
//	if err := db.WithTx(ctx, placeOrder); err != nil {
//	    if errors.Is(err, sqlb.ErrAfterCommit) {
//	        // The order exists. Something downstream of it did not fire.
//	        log.Error("order placed, notification failed", "err", err)
//	    } else {
//	        return err // The order does not exist.
//	    }
//	}
var ErrAfterCommit = errors.New("sqlb: transaction committed, but an after-commit callback failed")

// AfterCommit registers fn to run once this transaction commits, and not at all
// if it rolls back.
//
// This is where publishing an event, enqueuing a job or invalidating a cache
// belongs. AfterCreate and its siblings run inside the transaction, which is
// correct for validation — an error there rolls the write back — and wrong for
// anything the outside world can observe, because the transaction may still
// abort after the hook has already told the world it succeeded.
//
// Callbacks run in registration order after Commit returns nil, each receiving
// the context WithTx was called with. That context carries no transaction:
// there is nothing left to join, and handing back a committed one would be a
// trap.
//
// A failing callback does not stop the others — these are independent side
// effects, and abandoning the rest leaves more inconsistency rather than less.
// The failures are joined under ErrAfterCommit.
func (d *DB) AfterCommit(fn func(context.Context) error) error {
	if fn == nil {
		return errors.New("sqlb: AfterCommit called with a nil function")
	}
	if d.tx == nil {
		return errors.New("sqlb: AfterCommit needs a transaction to be after, " +
			"but this handle is not in one; wrap the write in db.WithTx")
	}
	d.tx.mu.Lock()
	defer d.tx.mu.Unlock()
	if d.tx.drained {
		return errors.New("sqlb: AfterCommit called after the transaction had already " +
			"committed; register it from inside the WithTx function or a hook it runs")
	}
	d.tx.afterCommit = append(d.tx.afterCommit, fn)
	return nil
}

// AfterCommit registers fn on the transaction carried by ctx. It is the form to
// use from a hook, which receives a context rather than a handle:
//
//	sqlb.On[Order]().AfterCreate(func(ctx context.Context, o *Order) error {
//	    id := o.ID
//	    return sqlb.AfterCommit(ctx, func(ctx context.Context) error {
//	        return events.Publish(ctx, OrderPlaced{ID: id})
//	    })
//	})
//
// Outside a transaction this is an error rather than an immediate call. "After
// commit" only means something when sqlb owns the commit; under autocommit the
// driver has already committed each statement and sqlb cannot say when, so a
// callback registered from BeforeCreate would fire before the insert and one
// registered from AfterCreate would fire after it. Running fn at a moment that
// depends on which hook happened to call it is the kind of quietly-wrong
// behaviour this codebase refuses elsewhere; the fix is one call, WithTx.
func AfterCommit(ctx context.Context, fn func(context.Context) error) error {
	tx, ok := TxFrom(ctx)
	if !ok {
		return errors.New("sqlb: AfterCommit found no transaction in the context; " +
			"the write it should follow must be inside db.WithTx")
	}
	return tx.AfterCommit(fn)
}

// drain runs the registered callbacks and reports their failures together.
// Called only after a successful commit; a rollback discards them by never
// reaching here.
func (s *txState) drain(ctx context.Context) error {
	s.mu.Lock()
	fns := s.afterCommit
	s.afterCommit = nil
	s.drained = true
	s.mu.Unlock()

	var errs []error
	for _, fn := range fns {
		if err := fn(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrAfterCommit, errors.Join(errs...))
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

// CanBeginTx reports whether WithTx would be able to open a transaction on this
// handle.
//
// It exists so that a caller who *requires* transactions can say so at startup
// rather than on the first write. `rest` uses it for exactly that: a resource
// that wraps its generated writes refuses to mount over an executor that cannot
// begin one, because discovering it at request time would mean the first POST
// is the error report.
//
// It reports true inside a transaction as well, where WithTx joins rather than
// begins.
func (d *DB) CanBeginTx() bool {
	if d.inTx {
		return true
	}
	_, ok := d.exec.(Beginner)
	return ok
}

// Tx returns the underlying *sql.Tx, if this handle runs on one.
//
// It exists so that a unit of work can be shared with a library that wants more
// than Executor's two methods. sqlc's generated DBTX wants four, so this is how
// both sides land on one transaction without giving up WithTx's rollback and
// panic handling:
//
//	err := db.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
//	    post, err := sqlb.InsertRows(&p).One(ctx, tx)
//	    if err != nil {
//	        return err
//	    }
//	    sqlTx, ok := tx.Tx()
//	    if !ok {
//	        return errors.New("expected a transaction")
//	    }
//	    return sqlcgen.New(sqlTx).RecordPublication(ctx, post.ID)
//	})
//
// It reports false when the executor is a pool, or a wrapper that does not
// expose the transaction it holds. Committing or rolling back the returned
// *sql.Tx directly is a mistake: WithTx owns that boundary, and doing it here
// leaves the after-commit callbacks unrun.
func (d *DB) Tx() (*sql.Tx, bool) {
	tx, ok := d.exec.(*sql.Tx)
	return tx, ok
}

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

	state := &txState{}
	tx := &DB{exec: sqlTx, hooks: d.hooks, inTx: true, tx: state}
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

	// Drained with ctx rather than txCtx: the transaction is over, so a
	// callback must not find a live handle through TxFrom.
	return state.drain(ctx)
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
