package sqlb

import (
	"context"
	"fmt"
	"reflect"
	"sync"
)

// Hooks are the domain-logic seams around a model's queries and mutations.
//
// The most load-bearing one is BeforeQuery. It receives the query itself, so a
// single registration applies a constraint to every read of that model —
// including reads issued by the generated REST handlers, which is how tenant
// scoping stops being something each call site has to remember:
//
//	sqlb.On[Post]().BeforeQuery(func(ctx context.Context, q *sqlb.Builder[Post]) error {
//	    org, ok := auth.OrgFrom(ctx)
//	    if !ok {
//	        return auth.ErrNoTenant
//	    }
//	    q.Where(sqlb.F("org_id").Eq(org))
//	    return nil
//	})
//
// Hooks are registered once at startup, typically from an init function or
// main, and run in registration order. A hook returning an error aborts the
// operation and the error reaches the caller unwrapped.
type Hooks[T any] struct {
	mu           sync.RWMutex
	beforeQuery  []func(context.Context, *Builder[T]) error
	beforeCreate []func(context.Context, *T) error
	afterCreate  []func(context.Context, *T) error
	beforeUpdate []func(context.Context, *Update[T]) error
	afterUpdate  []func(context.Context, []T) error
	beforeDelete []func(context.Context, *Delete[T]) error
	afterDelete  []func(context.Context, int64) error
}

// Registry holds the hook sets for a set of models, keyed by type.
//
// Most programs never name one: On[T]() reaches a process default, and
// registering at startup is the intended use. A registry becomes worth holding
// when two of them need to differ — a test that wants isolation without Reset,
// or a handle whose domain rules are not the process-wide ones. Attach it with
// DB.WithHooks.
type Registry struct {
	m sync.Map // reflect.Type -> *Hooks[T]
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// defaultRegistry is what On[T]() reaches, and what New gives a handle unless
// WithHooks says otherwise. It exists so that hooks registered before any
// handle was built still apply to every handle.
var defaultRegistry = NewRegistry()

// On returns the hook set for model T in the process-default registry,
// creating it on first use.
func On[T any]() *Hooks[T] {
	return OnIn[T](defaultRegistry)
}

// OnIn returns the hook set for model T in r, creating it on first use.
func OnIn[T any](r *Registry) *Hooks[T] {
	t := reflect.TypeOf((*T)(nil)).Elem()
	if v, found := r.m.Load(t); found {
		h, ok := v.(*Hooks[T])
		if !ok {
			panic(fmt.Sprintf("sqlb: hook registry holds %T for model %s", v, t))
		}
		return h
	}
	actual, _ := r.m.LoadOrStore(t, &Hooks[T]{})
	h, ok := actual.(*Hooks[T])
	if !ok {
		panic(fmt.Sprintf("sqlb: hook registry holds %T for model %s", actual, t))
	}
	return h
}

// BeforeQuery runs before every SELECT against T, including those issued by
// generated REST handlers. The hook may add predicates, joins or ordering.
//
// "Every SELECT against T" means every statement whose subject is T. It does
// not include T reached as the target of another model's expansion: joining
// `lists` for `?expand=list` does not run List's hooks, so a scope registered
// here constrains GET /lists and not the `list` an expanded task carries.
//
// This holds even when [ADR-0030]'s check has confirmed the hook exists — that
// check guards the target's own resource, and an expansion does not go through
// it. What bounds an expansion is the foreign key the parent row holds; see the
// expansion notes in expand.go for when that is and is not enough.
//
// [ADR-0030]: https://github.com/jryannel/sqlb/blob/main/docs/adr/0030-declared-scope-is-required.md
func (h *Hooks[T]) BeforeQuery(fn func(context.Context, *Builder[T]) error) *Hooks[T] {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beforeQuery = append(h.beforeQuery, fn)
	return h
}

// BeforeCreate runs on each row before insert, and may modify it: normalising
// an email, deriving a slug, stamping an owner.
func (h *Hooks[T]) BeforeCreate(fn func(context.Context, *T) error) *Hooks[T] {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beforeCreate = append(h.beforeCreate, fn)
	return h
}

// AfterCreate runs on each inserted row, with database defaults populated.
// It runs inside the caller's transaction, so returning an error rolls the
// insert back.
//
// That makes it right for validation and wrong for anything the outside world
// can observe — publishing an event, enqueuing a job, invalidating a cache —
// because the transaction may still abort after the hook has announced a write
// that then never happened. Register those with [AfterCommit] instead.
func (h *Hooks[T]) AfterCreate(fn func(context.Context, *T) error) *Hooks[T] {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.afterCreate = append(h.afterCreate, fn)
	return h
}

// BeforeUpdate runs before an update executes and receives the statement, so
// it can force columns (an updated_at stamp) or narrow the affected rows.
func (h *Hooks[T]) BeforeUpdate(fn func(context.Context, *Update[T]) error) *Hooks[T] {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beforeUpdate = append(h.beforeUpdate, fn)
	return h
}

// AfterUpdate receives the updated rows. Like AfterCreate it runs inside the
// transaction; side effects the outside world can see belong in [AfterCommit].
func (h *Hooks[T]) AfterUpdate(fn func(context.Context, []T) error) *Hooks[T] {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.afterUpdate = append(h.afterUpdate, fn)
	return h
}

// BeforeDelete runs before a delete executes and receives the statement.
func (h *Hooks[T]) BeforeDelete(fn func(context.Context, *Delete[T]) error) *Hooks[T] {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beforeDelete = append(h.beforeDelete, fn)
	return h
}

// AfterDelete receives the number of rows removed. Like AfterCreate it runs
// inside the transaction; side effects the outside world can see belong in
// [AfterCommit].
func (h *Hooks[T]) AfterDelete(fn func(context.Context, int64) error) *Hooks[T] {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.afterDelete = append(h.afterDelete, fn)
	return h
}

// RegisteredHooks reports which kinds of hook a model has, one bool per kind.
//
// It answers "did anyone write this" and deliberately not "does it do the right
// thing": a hook's body is a closure, and nothing here can tell a tenant
// predicate from a logging statement. That makes it useful for exactly one
// thing — refusing to serve a model whose schema declared an obligation that
// no registration could possibly be meeting, because there is no registration
// ([ADR-0030]).
//
// [ADR-0030]: https://github.com/jryannel/sqlb/blob/main/docs/adr/0030-declared-scope-is-required.md
type RegisteredHooks struct {
	BeforeQuery  bool
	BeforeCreate bool
	BeforeUpdate bool
	BeforeDelete bool
}

// Registered reports which kinds of hook are registered for T.
func (h *Hooks[T]) Registered() RegisteredHooks {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return RegisteredHooks{
		BeforeQuery:  len(h.beforeQuery) > 0,
		BeforeCreate: len(h.beforeCreate) > 0,
		BeforeUpdate: len(h.beforeUpdate) > 0,
		BeforeDelete: len(h.beforeDelete) > 0,
	}
}

// RegisteredFor reports which hooks are registered for T against whichever
// registry exec resolves to — the same resolution a query would get, so a
// handle carrying a scoped registry is asked about that registry rather than
// about the process default.
//
// It reads the registry at the moment it is called, which is why the check it
// exists for belongs where a resource is mounted: hooks registered afterwards
// are not visible to it, and a program that mounts before it registers is a
// program whose first request would have run unscoped anyway.
func RegisteredFor[T any](exec Executor) RegisteredHooks {
	return hooksFor[T](exec).Registered()
}

// Reset removes every registered hook for T. It exists for tests against the
// process-default registry, which otherwise leak registrations between cases.
// A test that can afford to name its own registry — NewRegistry, then
// DB.WithHooks — gets the same isolation without the teardown.
func (h *Hooks[T]) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Assigning a zero Hooks would overwrite the mutex that is currently held,
	// so the slices are cleared individually.
	h.beforeQuery, h.beforeCreate, h.afterCreate = nil, nil, nil
	h.beforeUpdate, h.afterUpdate = nil, nil
	h.beforeDelete, h.afterDelete = nil, nil
}

func (h *Hooks[T]) runBeforeQuery(ctx context.Context, b *Builder[T]) error {
	h.mu.RLock()
	fns := h.beforeQuery
	h.mu.RUnlock()
	for _, fn := range fns {
		if err := fn(ctx, b); err != nil {
			return err
		}
	}
	return b.err
}

func (h *Hooks[T]) runBeforeCreate(ctx context.Context, row *T) error {
	h.mu.RLock()
	fns := h.beforeCreate
	h.mu.RUnlock()
	for _, fn := range fns {
		if err := fn(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

func (h *Hooks[T]) runAfterCreate(ctx context.Context, rows []T) error {
	h.mu.RLock()
	fns := h.afterCreate
	h.mu.RUnlock()
	if len(fns) == 0 {
		return nil
	}
	for i := range rows {
		for _, fn := range fns {
			if err := fn(ctx, &rows[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Hooks[T]) runBeforeUpdate(ctx context.Context, u *Update[T]) error {
	h.mu.RLock()
	fns := h.beforeUpdate
	h.mu.RUnlock()
	for _, fn := range fns {
		if err := fn(ctx, u); err != nil {
			return err
		}
	}
	return u.err
}

func (h *Hooks[T]) runAfterUpdate(ctx context.Context, rows []T) error {
	h.mu.RLock()
	fns := h.afterUpdate
	h.mu.RUnlock()
	for _, fn := range fns {
		if err := fn(ctx, rows); err != nil {
			return err
		}
	}
	return nil
}

func (h *Hooks[T]) runBeforeDelete(ctx context.Context, d *Delete[T]) error {
	h.mu.RLock()
	fns := h.beforeDelete
	h.mu.RUnlock()
	for _, fn := range fns {
		if err := fn(ctx, d); err != nil {
			return err
		}
	}
	return d.err
}

func (h *Hooks[T]) runAfterDelete(ctx context.Context, n int64) error {
	h.mu.RLock()
	fns := h.afterDelete
	h.mu.RUnlock()
	for _, fn := range fns {
		if err := fn(ctx, n); err != nil {
			return err
		}
	}
	return nil
}
