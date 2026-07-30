module github.com/jryannel/sqlb/example/blog

// A module of its own for the same reason `rest` is one: it mounts the REST
// adapter, so it carries huma. Keeping it in the root module would put huma
// back in the engine's requirements through the back door, which is the whole
// thing ADR-0007's split exists to stop.
go 1.25.0

replace github.com/jryannel/sqlb => ../../

replace github.com/jryannel/sqlb/rest => ../../rest

require (
	github.com/danielgtaylor/huma/v2 v2.39.0
	github.com/jryannel/sqlb v0.0.0-00010101000000-000000000000
	github.com/jryannel/sqlb/rest v0.0.0-00010101000000-000000000000
)
