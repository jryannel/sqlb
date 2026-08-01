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

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jryannel/sqlb"
)

// Op is a bitmask of the operations a resource exposes.
//
// It mirrors schema.Op deliberately rather than importing it. Nothing on the
// request path may import the schema package — that is what keeps the runtime
// usable without the DSL — so the exposure decision crosses the line as a
// value, not as a type.
type Op uint8

const (
	OpCreate Op = 1 << iota
	OpRead      // GET /resource/{id}
	OpUpdate
	OpDelete
	OpList // GET /resource with filter, sort, search, pagination
)

// CRUD is the conventional single-row operation set. Combine it with OpList
// for a fully exposed collection.
const CRUD = OpCreate | OpRead | OpUpdate | OpDelete

// Has reports whether the mask contains op.
func (o Op) Has(op Op) bool { return o&op != 0 }

// CreateBody is what a POST body must be able to do: turn itself into a row.
//
// The conversion is the body type's job rather than the handler's because only
// the body knows which of its fields were meant for which column. Codegen emits
// one of these per creatable resource; a hand-written model supplies its own.
type CreateBody[T any] interface {
	// Row builds the row to insert. Returning an error rejects the request as
	// a 422, which is where cross-field validation belongs.
	Row() (*T, error)
}

// UpdateBody is what a PATCH body must be able to do: report which columns the
// request actually named.
//
// A typed struct cannot distinguish "absent" from "zero", which is the whole
// difficulty of PATCH, so the body type reports the change set explicitly.
// Codegen emits fields as pointers and returns only the non-nil ones.
type UpdateBody interface {
	// Changes maps column name to new value for the fields the request
	// carried. An empty map is rejected as a 400 rather than run as a no-op
	// update, because it almost always means the client sent the wrong shape.
	Changes() (map[string]any, error)
}

// None stands in for a body type on a resource that does not expose the
// corresponding operation. Its methods are never called, because the operation
// is never registered.
type None[T any] struct{}

// Row satisfies CreateBody and always fails, since a resource using None does
// not expose create.
func (None[T]) Row() (*T, error) {
	return nil, errors.New("rest: this resource does not expose create")
}

// Changes satisfies UpdateBody and always fails, since a resource using None
// does not expose update.
func (None[T]) Changes() (map[string]any, error) {
	return nil, errors.New("rest: this resource does not expose update")
}

// Options describes how one resource is exposed. It restates what the schema
// declared in schema.REST, and codegen writes it from that declaration.
type Options struct {
	// Path is the collection path, e.g. "/posts". Required.
	Path string

	// Ops is the set of exposed operations. Required: a resource exposing
	// nothing is a mistake rather than a way to hide one.
	Ops Op

	// Name is the singular resource name used in operation IDs and summaries,
	// e.g. "post" gives list-posts and get-post. Defaults to the path with its
	// leading slash removed.
	Name string

	// Tag groups the operations in the OpenAPI document. Defaults to Name.
	Tag string

	// Description documents the resource. It comes from the table's comment.
	Description string

	// Pagination and filter limits. Zero means the filter package's default.
	// MaxPageSize is a hard ceiling, not a hint: a client asking for more gets
	// the maximum rather than an error.
	DefaultPageSize int
	MaxPageSize     int
	MaxFilters      int
	MaxSortTerms    int
	// MaxOffset bounds how deep ?page= and ?offset= may reach into the result
	// set. Offset paging is the one dimension of a request whose cost grows
	// with the number the client sent, so it has a ceiling like the others; a
	// request past it is refused with a message pointing at ?cursor=.
	MaxOffset int

	// Expandable lists the relation names ?expand may name. Each must be a
	// relation the model declares — a `expands=` field beside an `expand`
	// column — and is checked at startup, because at request time an unknown
	// name would parse cleanly and answer 200 with the relation missing.
	//
	// Leaving it empty offers no ?expand at all, which is the right default: a
	// join is a cost, and a relation the schema happens to declare is not the
	// same thing as one this resource wants to serve.
	Expandable []string

	// DisableSearch rejects ?search even when columns are searchable.
	DisableSearch bool

	// DisableTransactions runs generated writes under autocommit.
	//
	// The default — wrapping each create, update and delete in a transaction —
	// is what makes sqlb.AfterCommit reachable from a generated write. Without
	// it there is no commit for a hook to be after, so a documented feature is
	// unreachable from the writes most applications actually issue
	// ([ADR-0021](../docs/adr/0021-hooks-receive-an-event.md)).
	//
	// The cost is a BEGIN/COMMIT round trip per write, and a server-side
	// connection held for longer. Behind PgBouncer in transaction pooling mode
	// that is a change in occupancy rather than only in latency
	// ([ADR-0019](../docs/adr/0019-pgbouncer-in-the-path.md)), so this exists
	// for anyone who measures it and decides against.
	//
	// Turning it on silently stops any AfterCommit callback the resource's
	// hooks register. Read that as the reason it is phrased as a disable rather
	// than as an enable: the safe value is the zero value.
	DisableTransactions bool

	// Security is the OpenAPI security requirement every operation of this
	// resource carries — the same shape huma.Operation.Security takes, so it is
	// a list of alternatives and each alternative names schemes and their
	// scopes:
	//
	//	Security: []map[string][]string{{"bearerAuth": {}}}
	//
	// It documents; it does not enforce. Authentication is middleware on the
	// router, and it runs whether or not this is set — leaving it empty produces
	// operations that are protected and do not say so, which is what every
	// consumer of the document has to guess about.
	//
	// The generated clients do not read this, and that is not an oversight: they
	// are generated from the schema rather than from the document, and they take
	// the credential from the transport the consuming project supplies. What
	// this is for is /docs, an agent reading the spec, and anything else driven
	// by the document.
	//
	// The scheme itself is declared once on the API, not here:
	//
	//	api.OpenAPI().Components.SecuritySchemes = map[string]*huma.SecurityScheme{
	//	    "bearerAuth": {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
	//	}
	Security []map[string][]string
}

func (o Options) name() string {
	if o.Name != "" {
		return o.Name
	}
	return strings.TrimPrefix(o.Path, "/")
}

func (o Options) tag() string {
	if o.Tag != "" {
		return o.Tag
	}
	return o.name()
}

func (o Options) validate() error {
	switch {
	case o.Path == "":
		return errors.New("rest: Options.Path is required")
	case !strings.HasPrefix(o.Path, "/"):
		return fmt.Errorf("rest: Options.Path %q must start with a slash", o.Path)
	case o.Ops == 0:
		return fmt.Errorf("rest: Options.Ops is empty for %s; a resource that exposes nothing should not be mounted", o.Path)
	}
	return nil
}

// Resource registers the exposed operations for model T on api.
//
// T is the row type, C the create body and U the update body. A resource that
// exposes neither create nor update passes rest.None[T] for both; the types are
// still instantiated, but Huma never sees them because the operations are not
// registered, so they stay out of the OpenAPI components.
//
// Registration is the startup path, so failures are returned rather than
// panicked: a mistake here should name the resource that caused it.
func Resource[T any, C CreateBody[T], U UpdateBody](api huma.API, db sqlb.Executor, opts Options) error {
	if err := opts.validate(); err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("rest: %s has no Executor", opts.Path)
	}

	b, err := bind[T](opts)
	if err != nil {
		return err
	}

	// Every single-row operation addresses a row by primary key, so a table
	// without one can only be listed. Saying so at startup is better than four
	// handlers that cannot be reached.
	if b.model.PK == nil && opts.Ops&(OpRead|OpUpdate|OpDelete) != 0 {
		return fmt.Errorf("rest: %s exposes %s but %s declares no primary key",
			opts.Path, opts.Ops&(OpRead|OpUpdate|OpDelete), b.model.Type)
	}

	// A schema that says these rows are confined has to be met by something
	// that confines them. See scope.go for what this does and does not prove.
	if err := checkObligations[T](b.model, db, opts); err != nil {
		return err
	}

	// Resolved once, at startup, so that an executor which cannot begin a
	// transaction is reported here rather than by the first write.
	w, err := newWriter(db, opts)
	if err != nil {
		return err
	}

	if opts.Ops.Has(OpList) {
		registerList(api, db, b)
	}
	if opts.Ops.Has(OpRead) {
		registerRead(api, db, b)
	}
	if opts.Ops.Has(OpCreate) {
		registerCreate[T, C](api, w, b)
	}
	if opts.Ops.Has(OpUpdate) {
		registerUpdate[T, U](api, w, b)
	}
	if opts.Ops.Has(OpDelete) {
		registerDelete(api, w, b)
	}
	return nil
}

// Must panics if err is non-nil. Generated registration code uses it, since a
// resource that cannot be mounted is a startup failure either way.
func Must(err error) {
	if err != nil {
		panic(err)
	}
}

// String renders the mask for diagnostics.
func (o Op) String() string {
	var parts []string
	for _, e := range []struct {
		op   Op
		name string
	}{
		{OpCreate, "create"}, {OpRead, "read"}, {OpUpdate, "update"},
		{OpDelete, "delete"}, {OpList, "list"},
	} {
		if o.Has(e.op) {
			parts = append(parts, e.name)
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "|")
}

// itemPath is the single-row path, e.g. "/posts/{id}". The template is always
// {id}, whatever the primary key column is called: the URL names the resource's
// identity, and renaming a column should not break every client.
func (o Options) itemPath() string { return o.Path + "/{id}" }

const (
	statusCreated = http.StatusCreated
	statusNoBody  = http.StatusNoContent
)
