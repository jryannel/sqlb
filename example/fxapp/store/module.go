// This file is hand-written; everything else in this package is generated,
// wiring_gen.go included (ADR-0059).
//
// It used to carry provideMigrations and provideOperations — the migration
// history wrapped for fxkit's group, and the resource mount wrapped for
// fxkit's other one, both properties of the schema and neither more than a
// fixed shape around what noteschema/sqlb.go already configures. That shape
// is what FxModule now generates: this file is what is left once the
// mechanical part is gone, which is naming the module and composing the one
// value the generated package hands it.
package store

import "go.uber.org/fx"

var Module = fx.Module("store", FxModule)
