// Package sqlbkit turns the connection dbbase opened into the two sqlb
// handles this application uses, and assembles the hook registry from what the
// feature modules contribute.
//
// This is the package the example exists to show. Everything else here is
// ordinary fx; what is specific to sqlb is that the domain rules — tenant
// scoping, soft deletes, invariants that must hold for every writer — live in
// a *sqlb.Registry, and a registry has to be complete before the first query
// is built against it. In a hand-written main that is a matter of writing the
// lines in the right order. Here it is a dependency edge, and the edge is what
// makes it impossible to get wrong from a module that has not been written
// yet: a module contributes its rules to the "hooks" value group, and the
// handle that carries them cannot be constructed until every contributor has
// run.
//
// sqlb makes that check load-bearing rather than advisory. A resource over a
// table whose schema declares a Scoped column refuses to mount unless the
// registrations its operations need are on the registry (ADR-0030) — so
// forgetting a hook is a boot failure naming the model, not a server that
// quietly serves one tenant's rows to another. app_test.go asserts exactly
// that by removing a module.
package sqlbkit

import "go.uber.org/fx"

var Module = fx.Module("sqlbkit",
	fx.Provide(
		fx.Annotate(NewUnscoped, fx.ResultTags(`name:"unscoped"`)),
		fx.Annotate(NewScoped, fx.ParamTags(`name:"unscoped"`, `group:"hooks"`)),
	),
)
