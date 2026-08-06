package sqlb

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Beginner is the subset of a pgx pool or connection that opens a transaction.
// It is asserted for rather than required, so Executor stays two methods and
// every wrapper written against it keeps working.
//
// *pgxpool.Pool and *pgx.Conn satisfy it. A wrapper that wants WithTx to work
// through it — a tracer, a pool adapter — implements this alongside Executor
// and returns the underlying pgx.Tx.
type Beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
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
		if d.inTx {
			// A pgx.Tx passed to New. sqlb runs in it but does not commit it,
			// and a callback registered here would wait for a commit this
			// package never performs.
			return errors.New("sqlb: AfterCommit cannot follow a transaction sqlb did not open; " +
				"this handle runs on a pgx.Tx the caller commits, so run the callback after " +
				"that Commit, or open the unit of work with db.WithTx instead")
		}
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
//	sqlb.On[Order](reg).AfterCreate(func(ctx context.Context, o *Order) error {
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

// New returns a handle over exec with an empty hook registry of its own.
//
// It acquires rules only from WithHooks, and there is no process-wide default
// for it to inherit (ADR-0047). Two calls to New produce two handles with
// nothing between them, so a handle cannot pick up rules some other part of
// the program registered — which also means registering hooks and then calling
// New is not enough on its own: name the registry.
//
//	reg := sqlb.NewRegistry()
//	sqlb.On[Post](reg).BeforeQuery(scopeToOrg)
//	db := sqlb.New(pool).WithHooks(reg)
//
// A pgx.Tx is an Executor like any other, and passing one is how sqlb joins a
// transaction the application opened itself:
//
//	tx, err := pool.Begin(ctx)
//	defer tx.Rollback(ctx)
//	if err := legacy.DebitAccount(ctx, tx, id); err != nil {
//	    return err
//	}
//	_, err = sqlb.InsertRows(&entry).Exec(ctx, sqlb.New(tx))
//	...
//	return tx.Commit(ctx)
//
// The handle knows it is inside one, so InTx reports true and a WithTx on it
// joins rather than opening a second transaction against the same pool. What it
// deliberately does not do is take over the boundary: the caller opened the
// transaction and the caller commits it. So AfterCommit refuses here rather
// than accumulating callbacks nothing will ever drain — WithTx is what owns a
// commit, and therefore the only thing that can promise anything after one.
func New(exec Executor) *DB {
	if exec == nil {
		panic("sqlb: New called with a nil Executor")
	}
	// tx stays nil: it is the state of a transaction *this* package will
	// commit, and a borrowed one is not that.
	if _, borrowed := exec.(pgx.Tx); borrowed {
		return &DB{exec: exec, hooks: NewRegistry(), inTx: true}
	}
	return &DB{exec: exec, hooks: NewRegistry()}
}

// WithHooks returns a copy of the handle resolving hooks against r.
//
// This is how a handle acquires rules at all, since New gives one an empty
// registry of its own. It is also how two tenants-worth of differing domain
// rules coexist in one process, and how a test gets isolation.
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

// Tx returns the underlying pgx.Tx, if this handle runs on one.
//
// It exists so that a unit of work can be shared with code that wants more than
// Executor's two methods — CopyFrom, SendBatch, or a generated query set — so
// both sides land on one transaction without giving up WithTx's rollback and
// panic handling:
//
//	err := db.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
//	    post, err := sqlb.InsertRows(&p).One(ctx, tx)
//	    if err != nil {
//	        return err
//	    }
//	    pgTx, ok := tx.Tx()
//	    if !ok {
//	        return errors.New("expected a transaction")
//	    }
//	    return queries.New(pgTx).RecordPublication(ctx, post.ID)
//	})
//
// It reports false when the executor is a pool, or a wrapper that does not
// expose the transaction it holds. Committing or rolling back the returned
// pgx.Tx directly is a mistake: WithTx owns that boundary, and doing it here
// leaves the after-commit callbacks unrun.
func (d *DB) Tx() (pgx.Tx, bool) {
	tx, ok := d.exec.(pgx.Tx)
	return tx, ok
}

// Query satisfies Executor.
func (d *DB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return d.exec.Query(ctx, query, args...)
}

// Exec satisfies Executor.
func (d *DB) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return d.exec.Exec(ctx, query, args...)
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
	return d.WithTxOptions(ctx, pgx.TxOptions{}, fn)
}

// WithTxOptions is WithTx with an explicit isolation level or read-only flag.
//
// The options are ignored when joining an outer transaction, since isolation is
// a property of the transaction and the outer one has already begun. Asking for
// stricter isolation than the enclosing transaction provides is therefore an
// error rather than a silent downgrade.
func (d *DB) WithTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context, tx *DB) error) error {
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
			"but %T only implements Executor; pass the *pgxpool.Pool itself, or implement "+
			"BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) on the wrapper", d.exec)
	}
	pgTx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("sqlb: beginning transaction: %w", err)
	}

	state := &txState{}
	tx := &DB{exec: pgTx, hooks: d.hooks, inTx: true, tx: state}
	txCtx := context.WithValue(ctx, txKey{}, tx)

	// Rolling back is a statement of its own, and pgx wants a context for it.
	// The caller's is the wrong one: the usual reason a unit of work fails is
	// that its context was cancelled, and rolling back on a cancelled context
	// would abandon the transaction open on the connection rather than end it.
	abortCtx := context.WithoutCancel(ctx)

	// A panic must not leave the transaction open, and it must still reach the
	// caller: rollback, then re-raise with the original stack.
	committed := false
	defer func() {
		if committed {
			return
		}
		if p := recover(); p != nil {
			_ = pgTx.Rollback(abortCtx)
			panic(p)
		}
	}()

	if err := fn(txCtx, tx); err != nil {
		if rbErr := pgTx.Rollback(abortCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			// Both matter: the caller's error says why the unit of work
			// failed, the rollback error says the connection may be unusable.
			return errors.Join(err, fmt.Errorf("sqlb: rolling back: %w", rbErr))
		}
		return err
	}

	if err := pgTx.Commit(ctx); err != nil {
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
func compatibleWithOuter(opts pgx.TxOptions) error {
	if opts.IsoLevel != "" {
		return fmt.Errorf("sqlb: WithTxOptions asked for %s inside an existing transaction, "+
			"whose isolation is already fixed; request it on the outermost WithTx instead",
			opts.IsoLevel)
	}
	// ReadOnly is a narrowing, but Postgres will not accept it mid-transaction
	// either, and pretending it applied would be worse than saying so.
	if opts.AccessMode == pgx.ReadOnly {
		return errors.New("sqlb: WithTxOptions asked for a read-only transaction inside an " +
			"existing one, which cannot be narrowed after it has begun; request it on the " +
			"outermost WithTx instead")
	}
	// BeginQuery replaces the BEGIN statement outright, which cannot mean
	// anything when no BEGIN is about to be sent.
	if opts.BeginQuery != "" {
		return errors.New("sqlb: WithTxOptions gave a BeginQuery inside an existing " +
			"transaction, which has already begun; give it on the outermost WithTx instead")
	}
	return nil
}

// txKey addresses the transaction handle in a context.
type txKey struct{}

// TxFrom returns the transaction handle a hook is running under, if any.
//
// This is what lets a hook take part in the unit of work it was triggered by,
// and it is the whole of a hook's reach beyond its own model: the write hooks
// receive a row or a statement rather than a handle, so anything a hook does to
// another table it does through this one.
//
// The nearer use is reading rows written earlier in the same transaction.
// Reading through the process-wide pool would miss them, because they are not
// committed yet:
//
//	sqlb.On[Post](reg).BeforeCreate(func(ctx context.Context, p *Post) error {
//	    tx, ok := sqlb.TxFrom(ctx)
//	    if !ok {
//	        return errors.New("posts must be created inside a transaction")
//	    }
//	    n, err := sqlb.Query[Post]().Where(sqlb.F("slug").Eq(p.Slug)).Count(ctx, tx)
//	    ...
//	})
//
// # The rules a statement issued here runs under
//
// The handle is the one the *request* resolved against, so a statement issued
// through it runs that request's hooks — including those of the model being
// reached for. For a consequence on the same scoping axis that is exactly right:
// a comment's AfterCreate bumping its task's counter wants the caller's
// workspace predicate on the update, because the comment and the task are in one
// workspace. That is example/tasks, and it is why this is the default.
//
// Where the two models are scoped on *different* axes it is a trap. A buyer's
// order decrementing a shop's stock picks up the stock table's request-facing
// BeforeUpdate — the one that keeps a buyer from patching a shop's rows — and
// matches nothing, and [Update.Exec] reports zero rows without an error, so the
// order commits with the stock untouched.
//
// Two habits close that. Run the consequence on a handle whose rules are the
// consequence's, with [DB.WithHooks] over a registry built for it — the
// statement stays on this transaction and only the rules change. And prefer
// [Update.One], which returns [ErrNotFound] rather than nothing at all when the
// row that had to move did not.
func TxFrom(ctx context.Context) (*DB, bool) {
	tx, ok := ctx.Value(txKey{}).(*DB)
	return tx, ok
}

// hooksFor resolves the hook set for T against whichever registry the executor
// carries. It is the one place that knows hooks can be scoped, so terminal
// methods keep taking a plain Executor.
func hooksFor[T any](exec Executor) *Hooks[T] {
	return On[T](registryOf(exec))
}

// registryOf is hooksFor without the type parameter, for the one caller that
// does not have one: an expansion reaches its target through a *Model and looks
// the target's hooks up by reflect.Type.
//
// An Executor that is not a *DB — a raw pool, a borrowed pgx.Tx — carries no
// registry, and resolves to one nothing can register into. Saying "no rules"
// is the honest answer for a handle-less statement; it used to say "whatever
// the process registered", which is how a query issued against a bare pool
// could be confined by rules its call site never mentioned, and how one that
// expected confinement lost it when those rules moved.
func registryOf(exec Executor) *Registry {
	if d, ok := exec.(*DB); ok {
		return d.hooks
	}
	return noHooks
}
