package rest

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/jryannel/sqlb"
)

// PublishChanges makes every write of T announce itself to p.
//
// It registers hooks rather than wrapping the REST handlers, and that is the
// design rather than an implementation detail. Hooks are keyed by type and run
// inside the mutation, so one registration covers the generated CRUD handlers,
// the generated actions, and the application's own sqlb writes alike — the same
// reason a BeforeQuery hook is what scopes reads instead of each handler
// remembering to. A change feed fed only by the REST layer would go quiet for
// exactly the writes most likely to matter: the background job, the migration,
// the admin script.
//
// Wire it once at startup, beside the resources:
//
//	broker := rest.NewBroker(rest.BrokerOptions{})
//	rest.Must(rest.PublishChanges[blog.Post](broker))
//	rest.Must(rest.Events(srv.API, rest.EventsOptions{Source: broker}))
//
// # When the event is published
//
// After the transaction commits, through [sqlb.AfterCommit]. Announcing from
// inside the mutation would publish changes that then rolled back, and a client
// refetching on one of those would see the row unchanged and cache the
// contradiction.
//
// That requires the write to be in a transaction, which generated writes are by
// default ([Options.DisableTransactions] is what turns it off). Under
// autocommit there is no commit left to be after — the statement is already
// durable when the hook runs — so the event is published immediately. The
// distinction is real but not visible to a subscriber.
//
// # Multi-tenancy
//
// When the model declares a `scope` column ([ADR-0030]), each event carries that
// column's value in [Event.Scope] — off the wire, for
// [EventsOptions.Filter] to compare against the subscriber's tenant. Without it
// a filter has nothing to decide on, because an invalidation names a row and not
// the tenant that owns it.
//
// A soft delete is an UPDATE, so it carries both the key and the scope. A hard
// delete carries neither; see below.
//
// # What a delete announces
//
// A table, with no key. sqlb's AfterDelete hook receives the number of rows
// removed rather than the rows themselves, so the key is not available to
// publish. A subscriber reads the keyless event as "invalidate this
// collection", which is what a delete requires of it regardless: the row is
// gone and the list it was in has changed.
//
// # Which registry
//
// r is the registry the announcing hooks are registered into, and it must be
// the one the handle doing the writing resolves against — the registry passed
// to [sqlb.DB.WithHooks]. Publishing into a registry no handle carries
// registers hooks nothing will ever run, which looks exactly like a working
// invalidation feed that never emits.
//
// This used to default to a process-wide registry, with the registry-taking
// form under a longer name. Removing that default was [ADR-0047].
//
// [ADR-0030]: https://github.com/jryannel/sqlb/blob/main/docs/adr/0030-declared-scope-is-required.md
// [ADR-0047]: https://github.com/jryannel/sqlb/blob/main/docs/adr/0047-no-default-hook-registry.md
func PublishChanges[T any](r *sqlb.Registry, p Publisher) error {
	if r == nil {
		return errors.New("rest: PublishChanges needs a registry")
	}
	return publishChanges(sqlb.On[T](r), p)
}

func publishChanges[T any](h *sqlb.Hooks[T], p Publisher) error {
	if p == nil {
		return errors.New("rest: PublishChanges needs a Publisher")
	}
	m := sqlb.ModelOf[T]()
	if m.Table == "" {
		return fmt.Errorf("rest: %s has no table name to publish under", m.Type)
	}
	table := m.Table
	pk := m.PK
	// The column the schema declared `scope`, or nil. Reading it here rather
	// than per event keeps ADR-0030's obligation on this path too: the model
	// already says which column confines its rows, so the feed can say which
	// tenant an event belongs to without the endpoint knowing what a tenant is.
	scope := m.Scope

	h.AfterCreate(func(ctx context.Context, row *T) error {
		return announce(ctx, p, Event{
			Table: table,
			Key:   keyOf(pk, row),
			Op:    Created,
			Scope: keyOf(scope, row),
		})
	})

	h.AfterUpdate(func(ctx context.Context, rows []T) error {
		events := make([]Event, len(rows))
		for i := range rows {
			events[i] = Event{
				Table: table,
				Key:   keyOf(pk, &rows[i]),
				Op:    Updated,
				Scope: keyOf(scope, &rows[i]),
			}
		}
		return announce(ctx, p, events...)
	})

	h.AfterDelete(func(ctx context.Context, n int64) error {
		// A delete that matched nothing changed nothing. Announcing it would
		// have every subscriber refetch a collection that is identical.
		if n == 0 {
			return nil
		}
		return announce(ctx, p, Event{Table: table, Op: Deleted})
	})

	return nil
}

// announce publishes after the commit when there is one to be after, and
// immediately when there is not.
//
// The fallback is not a silent downgrade of the guarantee: under autocommit the
// statement committed before the hook was called, so publishing now is publishing
// after the commit. What it does lose is atomicity across a multi-statement unit
// of work, which under autocommit does not exist to lose.
func announce(ctx context.Context, p Publisher, events ...Event) error {
	if len(events) == 0 {
		return nil
	}
	if _, inTx := sqlb.TxFrom(ctx); !inTx {
		p.Publish(events...)
		return nil
	}
	return sqlb.AfterCommit(ctx, func(context.Context) error {
		p.Publish(events...)
		return nil
	})
}

// keyOf renders one of a row's columns the way a URL renders it, so that a
// subscriber can concatenate the primary key onto the resource path to refetch.
// A nil column — no primary key, or no declared scope — reads as empty.
func keyOf[T any](col *sqlb.ColumnInfo, row *T) string {
	if col == nil || row == nil {
		return ""
	}
	field, ok := fieldAt(reflect.ValueOf(row).Elem(), col.Index)
	if !ok {
		return ""
	}
	return keyString(field)
}

func keyString(v reflect.Value) string {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if !v.CanInterface() {
		return ""
	}
	value := v.Interface()
	// A []byte key is bytes of text, not a number list: fmt would render it as
	// "[104 101 108 108 111]" and the client would ask for a row by that.
	if b, ok := value.([]byte); ok {
		return string(b)
	}
	// Everything else goes through fmt, which reaches String() on the uuid and
	// time types a key is otherwise likely to be.
	return fmt.Sprint(value)
}
