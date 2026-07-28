package rest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jryannel/sqlb"
)

// The obligation check.
//
// A generated handler is safe to trust because a BeforeQuery hook constrains
// every read it issues ([ADR-0008]). That argument has a hole in it, and the
// hole is the case where nobody registered the hook: add a table, expose it,
// forget the registration, and the resource serves every tenant's rows with a
// 200 next to them. Nothing fails, which is the worst available outcome —
// row-level security, the thing hooks are offered as an alternative to, is
// default-deny, and this was default-open.
//
// So a schema declaration that says rows are confined — schema.Field.Scoped,
// or schema.SoftDelete — becomes an obligation on the resource that exposes
// the table, and mounting one whose obligations have no hook behind them is a
// startup error ([ADR-0030]).
//
// What this checks is that a hook exists, not that it does anything. A
// BeforeQuery hook that logs and returns nil satisfies it. That is a real
// limit and it is the whole design: the alternative is executing the
// application's hooks at startup against a fabricated context to see what
// predicate falls out, which runs user code with a request that never happened
// in order to guess at its intent. One bit, honestly described, is worth more
// than a check that can be fooled in a way nobody expects.
//
// [ADR-0008]: https://github.com/jryannel/sqlb/blob/main/docs/adr/0008-hooks-as-domain-seam.md
// [ADR-0030]: https://github.com/jryannel/sqlb/blob/main/docs/adr/0030-declared-scope-is-required.md

// obligation is one hook a resource's declarations require, and the reasons it
// is required. Reasons accumulate because two declarations can want the same
// hook, and an error naming both is more useful than an error naming one.
type obligation struct {
	hook    string
	ops     string
	because []string
}

// checkObligations reports the declarations of m that opts leaves unmet.
//
// The mapping from operation to hook is not decoration. A BeforeQuery hook
// constrains what a request can see and says nothing about what it can
// overwrite by id, so an exposed update needs its own registration — which is
// the arrangement example/tasks arrived at by hand, and this is that
// arrangement made compulsory.
func checkObligations[T any](m *sqlb.Model, exec sqlb.Executor, opts Options) error {
	if m.Scope == nil && m.Soft == nil {
		return nil
	}

	var reads []string
	if m.Scope != nil {
		reads = append(reads, fmt.Sprintf("%s is Scoped", m.Scope.Name))
	}
	if m.Soft != nil {
		reads = append(reads, fmt.Sprintf("%s declares a soft delete", m.Soft.Name))
	}

	required := map[string]*obligation{}
	require := func(hook, ops string, because []string) {
		if len(because) == 0 {
			return
		}
		o, found := required[hook]
		if !found {
			o = &obligation{hook: hook, ops: ops}
			required[hook] = o
		}
		o.because = append(o.because, because...)
	}

	// Reads. Both declarations want this one: a scoped table wants the tenant
	// predicate, and a soft-deleted table wants deleted_at filtered back out.
	if opts.Ops.Has(OpList) || opts.Ops.Has(OpRead) {
		require("BeforeQuery", "list and read", reads)
	}

	// Writes address a row by primary key, so the read predicate is not in the
	// statement and each needs its own. Only the tenant declaration asks for
	// these: a soft delete constrains what is visible, not what is writable,
	// and a table that means DELETE to be soft leaves OpDelete out entirely.
	var scope []string
	if m.Scope != nil {
		scope = []string{fmt.Sprintf("%s is Scoped", m.Scope.Name)}
	}
	if opts.Ops.Has(OpUpdate) {
		require("BeforeUpdate", "update", scope)
	}
	if opts.Ops.Has(OpDelete) {
		require("BeforeDelete", "delete", scope)
	}
	// A Scoped column is ReadOnly — the schema validator requires it — so it is
	// absent from the generated create body and a BeforeCreate hook is the only
	// thing that can supply it. Without one the insert reaches the database
	// with no tenant at all.
	if opts.Ops.Has(OpCreate) {
		require("BeforeCreate", "create", scope)
	}

	have := sqlb.RegisteredFor[T](exec)
	registered := map[string]bool{
		"BeforeQuery":  have.BeforeQuery,
		"BeforeCreate": have.BeforeCreate,
		"BeforeUpdate": have.BeforeUpdate,
		"BeforeDelete": have.BeforeDelete,
	}

	var missing []*obligation
	for hook, o := range required {
		if !registered[hook] {
			missing = append(missing, o)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].hook < missing[j].hook })

	// Every unmet obligation at once, with the registration that would satisfy
	// it — the same courtesy a rejected filter gets, and for the same reason:
	// one round trip per mistake is a bad way to learn a rule (ADR-0011).
	var b strings.Builder
	fmt.Fprintf(&b, "rest: %s exposes %s, and nothing confines %s", opts.Path, opts.Ops, m.Type)
	for _, o := range missing {
		fmt.Fprintf(&b, "\n  %s: %s is not registered (%s)",
			o.ops, o.hook, strings.Join(dedupe(o.because), "; "))
	}
	b.WriteString("\n  register them on the registry this handle resolves against, before mounting;" +
		"\n  or drop the declaration, which is the honest way to say the rows are not confined")
	return fmt.Errorf("%s", b.String())
}

func dedupe(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	out := ss[:0:0]
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
