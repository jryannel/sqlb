// Package rest mounts a schema-declared resource on a Huma API.
//
// One generic function serves every resource. Resource[T, C, U] instantiates
// the same handlers for each model, and the OpenAPI document is still precise
// per resource, because the operation's parameters are built from the model's
// capabilities rather than from a Go struct. A resource that declares three
// filterable columns documents exactly three filter parameters, with the
// operators its column types accept.
//
// That is what makes the compositional filter grammar describable: `?age=gte.18`
// is not a fixed parameter set, but the *columns* are fixed, and enumerating one
// parameter per filterable column is both precise and finite.
//
//	srv := rest.NewServer(rest.Config{Title: "Blog", Version: "1.0.0"})
//	blog.Register(srv.API, db)                 // generated: one Resource call per table
//	http.ListenAndServe(":8080", srv.Handler)
//
// NewServer is the batteries-included path: a huma.API on net/http, with the
// OpenAPI document and docs page served for you, and no third-party router.
// Under it, each exposed table is one rest.Resource call, generated into
// rest_gen.go:
//
//	rest.Must(rest.Resource[blog.Post, blog.PostCreate, blog.PostPatch](srv.API, db, rest.Options{
//	    Path: "/posts",
//	    Ops:  rest.CRUD | rest.OpList,
//	}))
//
// NewServer is a convenience over a seam, not a replacement for it: Resource and
// the generated Register take a huma.API, so an application that wants chi, gin
// or echo — for that router's middleware — builds the API itself with the
// matching adapter (humachi.New(router, ...)) and passes it instead. The choice
// of router stays the application's. Nothing here imports the schema package:
// exposure reaches the runtime as an Options value, the way capabilities reach it
// as struct tags.
//
// # Reads are hooked
//
// Every read goes through sqlb.Query[T], so a BeforeQuery hook registered on T
// applies to the REST surface too. Tenant scoping is therefore a startup
// registration rather than something each handler has to remember. This is the
// reason registration is generic over T instead of reflective: hooks are keyed
// by type, and a reflective dispatcher could not run them.
package rest
