// Package pgtest runs sqlb against a real Postgres.
//
// Everything here needs a container, and the engine's dependency list is two
// entries long — pgx, and huma for `rest` — an invariant `mise run deps-check`
// enforces so that a consumer importing sqlb inherits nothing it did not choose
// (ADR-0040). That is why this is a module of its own rather than a build tag
// or a test file in `introspect`.
//
// The reason survived the driver flip with one word changed. sqlb depends on
// pgx now, so pgx alone would no longer justify the split — testcontainers
// does, and docker/docker and the forty modules behind it do.
//
// # Why a separate module, and not just test-only imports
//
// `deps-check` runs `go list -deps`, which does not report test-only imports.
// Adding testcontainers to the root module as a test dependency would therefore
// leave the gate reporting a short list while go.mod grew docker/docker and
// forty other modules. A guard that keeps reporting success after it stops
// covering anything is the failure mode ADR-0016 exists to prevent, and one
// this repository has already hit three times. A nested module is excluded from
// the parent's `go list ./...` by construction, so the invariant stays literally
// true instead of true-with-an-exemption.
//
// # What this is for
//
// The engine's own tests use an in-memory executor and need no database, which
// is a property worth keeping: it is what makes `mise run test` a fast inner
// loop. What that cannot answer is whether the SQL sqlb generates is *valid*
// rather than merely *expected*. Golden tests compare rendered DDL against a
// string somebody wrote; these apply it to Postgres and let Postgres judge.
//
// ADR-0040's port is the sharpest illustration this module has: both bugs the
// driver flip introduced — a catalog column misread, and a rejected write
// reported as a mapping error — were invisible to a canned result set and
// caught here.
//
// ADR-0014 holds at Medium confidence for exactly this reason — its round-trip
// was measured by hand, and the scripts that measured it are gone. The tests
// here are that measurement, committed.
//
// # Running
//
//	mise run test-pg
//
// Requires a working Docker daemon. There is deliberately no skip-when-absent
// path: a suite that silently passes when it cannot reach a database is worse
// than one that fails, because it reports coverage it does not have.
package pgtest
