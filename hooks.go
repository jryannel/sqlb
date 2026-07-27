package sqlb

import (
	"context"
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

var hookRegistry sync.Map // reflect.Type -> *Hooks[T]

// On returns the hook set for model T, creating it on first use.
func On[T any]() *Hooks[T] {
	t := reflect.TypeOf((*T)(nil)).Elem()
	if v, ok := hookRegistry.Load(t); ok {
		return v.(*Hooks[T])
	}
	actual, _ := hookRegistry.LoadOrStore(t, &Hooks[T]{})
	return actual.(*Hooks[T])
}

// BeforeQuery runs before every SELECT against T, including those issued by
// generated REST handlers. The hook may add predicates, joins or ordering.
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

// AfterUpdate receives the updated rows.
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

// AfterDelete receives the number of rows removed.
func (h *Hooks[T]) AfterDelete(fn func(context.Context, int64) error) *Hooks[T] {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.afterDelete = append(h.afterDelete, fn)
	return h
}

// Reset removes every registered hook for T. It exists for tests, which
// otherwise leak registrations between cases.
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
