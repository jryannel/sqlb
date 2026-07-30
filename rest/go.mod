module github.com/jryannel/sqlb/rest

// The HTTP adapter is a module of its own so that importing the engine does not
// put huma in a consumer's module graph — nor huma's `go` directive on their
// toolchain. See ADR-0007. The floor here is huma's, not ours.
go 1.25.0

replace github.com/jryannel/sqlb => ../

require (
	github.com/danielgtaylor/huma/v2 v2.39.0
	github.com/jryannel/sqlb v0.0.0-00010101000000-000000000000
)
