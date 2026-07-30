package sqlbkit

import (
	"fmt"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jryannel/sqlb"

	"github.com/jryannel/sqlb/example/fxapp/dbbase"
)

// HookSet is the value-group element a module contributes to register its
// query hooks.
//
// A module provides one like this:
//
//	fx.Provide(fx.Annotate(provideHooks, fx.ResultTags(`group:"hooks"`)))
//
// Register may fail: a rule that cannot be expressed is better reported at
// boot than skipped, and the group's whole purpose is that nobody downstream
// gets a handle until every contributor has had its say.
type HookSet struct {
	// Module names the contributor. It appears in the boot log and in the
	// error when Register fails, which is the difference between "a hook
	// failed" and a file to open.
	Module string

	// Register adds this module's rules to the registry.
	Register func(*sqlb.Registry) error
}

// NewUnscoped is the handle with no hooks on it.
//
// It exists for the two jobs that cannot be scoped because they run before
// there is anything to scope by: provisioning the configured spaces at boot,
// and looking a space up by slug to decide what the scope *is*. The equivalent
// in example/tasks is the handle that serves register and login.
//
// It is a separate value rather than a flag on the scoped handle, and that is
// the point: a flag is something a caller passes, and the set of callers
// allowed to pass it is exactly the thing being controlled. Two values, one of
// which is asked for by name and appears in three files, is harder to reach
// for by accident. Grep for `name:"unscoped"` to see every consumer.
//
// The Migrated parameter is not used. It is here because this handle is how
// the boot-time provisioning reaches the database, and a query against a table
// that does not exist yet is the failure it rules out.
func NewUnscoped(pool *pgxpool.Pool, _ dbbase.Migrated) *sqlb.DB {
	// WithHooks on a fresh registry rather than sqlb.New alone: New resolves
	// against the process-wide default registry, which nothing in this
	// application registers into but a library in some future dependency
	// might. An empty registry says "no rules apply here" out loud.
	return sqlb.New(pool).WithHooks(sqlb.NewRegistry())
}

// NewScoped is the handle everything else uses: the same connection, with
// every module's rules attached.
//
// The registry is built here, from the value group, rather than by a function
// each module calls at init time. That is what makes two servers in one test
// binary independent — each fx app builds its own registry — and it is why
// this constructor can report which module's registration failed.
func NewScoped(unscoped *sqlb.DB, sets []HookSet, log *slog.Logger) (*sqlb.DB, error) {
	// Sorted so the boot log reads the same way twice. Hook registration is
	// order-independent — a BeforeQuery hook adds a predicate, and predicates
	// commute — so this is for the reader, not for correctness.
	ordered := append([]HookSet(nil), sets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Module < ordered[j].Module })

	reg := sqlb.NewRegistry()
	names := make([]string, 0, len(ordered))
	for _, set := range ordered {
		if set.Register == nil {
			return nil, fmt.Errorf("sqlbkit: the %q hook set has no Register function", set.Module)
		}
		if err := set.Register(reg); err != nil {
			return nil, fmt.Errorf("sqlbkit: registering %s hooks: %w", set.Module, err)
		}
		names = append(names, set.Module)
	}

	log.Info("sqlbkit: hooks registered", "modules", names)
	return unscoped.WithHooks(reg), nil
}
