package codegen

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// This file emits the TypeScript client described by ADR-0028: row types,
// typed request parameters, a URL encoder for the filter grammar, a key
// factory, and TanStack Query option factories.
//
// It is generated from the same registry the Go emitters read rather than from
// the emitted OpenAPI document, because the document is lossy exactly where the
// value is. `?status=eq.published` documents as `array<string>` with the
// operator vocabulary in prose, so a generic generator emits `status?: string[]`
// and `status=bogus.x` compiles. The registry knows the operators each column
// type accepts, so the client can refuse it at the TypeScript compile step —
// which is the typed column facade's property (ADR-0009) carried across the
// wire.
//
// Two files, because the layers are meant to be usable separately:
//
//   - client.gen.ts   types, encoder, transport functions, key factory. No
//     imports at all, so it typechecks in any project.
//   - queries.gen.ts  queryOptions and infiniteQueryOptions, which take
//     @tanstack/react-query as a peer dependency.
//
// What is deliberately not emitted: the client shell, hooks, mutation helpers
// and optimistic updates. Auth, refresh, retry and redirect-on-401 are not
// derivable from a schema, so the transport is injected — the same seam
// argument `rest` makes by mounting onto a huma.API the application built.

// renderTSClient emits layers 1 to 3, plus the key factory.
func renderTSClient(opts Options) ([]byte, error) {
	resources, err := tsResources(opts.Registry)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	b.WriteString(tsClientHeader)
	b.WriteString(tsRuntime)

	// Row types for every table, not only the exposed ones: an expansion can
	// name a table that has no endpoint of its own, and the row still has to
	// have a type. This is the same call `.Expandable()` already makes on the
	// server.
	for _, t := range opts.Registry.Tables() {
		tsRowTypes(&b, opts.Registry, t)
	}

	for _, r := range resources {
		tsResourceSection(&b, r)
	}

	tsKeyIndex(&b, resources)
	return b.Bytes(), nil
}

// renderTSQueries emits layer 4. It returns nil when nothing is exposed, so a
// schema with no REST surface does not acquire a file that imports TanStack
// Query for the sake of an empty object.
func renderTSQueries(opts Options) ([]byte, error) {
	all, err := tsResources(opts.Registry)
	if err != nil {
		return nil, err
	}
	// A resource exposing neither list nor read has no reads to offer options
	// for, and a file of empty factories is worse than no file.
	var resources []tsResource
	for _, r := range all {
		if r.ops.Has(schema.OpList) || r.ops.Has(schema.OpRead) {
			resources = append(resources, r)
		}
	}
	if len(resources) == 0 {
		return nil, nil
	}

	var b bytes.Buffer
	fmt.Fprint(&b, tsQueriesHeader)

	// Types and values are imported separately, because a project with
	// `verbatimModuleSyntax` needs a type import to say so — and because it
	// makes it visible that this file adds behaviour to types it does not own.
	types := []string{"Transport"}
	values := []string{}
	for _, r := range resources {
		values = append(values, r.ident+"Keys")
		if r.hasExpand() {
			types = append(types, r.typeName+"Expand")
		}
		if r.ops.Has(schema.OpList) {
			types = append(types, r.typeName+"Column", r.typeName+"ListParams")
			values = append(values, "list"+r.plural)
		}
		if r.ops.Has(schema.OpRead) {
			types = append(types, r.typeName+"GetParams")
			values = append(values, "get"+r.typeName)
		}
	}
	tsImportList(&b, "import type", types, opts.tsClientImport())
	tsImportList(&b, "import", values, opts.tsClientImport())

	for _, r := range resources {
		tsQueriesSection(&b, r)
	}
	return b.Bytes(), nil
}

// tsImportList writes one import statement, sorted and one name per line, so
// that adding a table produces a one-line diff rather than a reflowed one.
func tsImportList(b *bytes.Buffer, keyword string, names []string, from string) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(b, "%s {\n", keyword)
	for _, name := range sortedSet(uniqueSet(names)) {
		fmt.Fprintf(b, "  %s,\n", name)
	}
	fmt.Fprintf(b, "} from %s;\n", tsString(from))
}

// tsResource is everything about one exposed table the emitter needs, resolved
// once so the templates below read as output rather than as lookups.
type tsResource struct {
	table    *schema.TableDef
	typeName string // Task
	ident    string // task
	plural   string // Tasks
	path     string
	ops      schema.Op

	filterable []*schema.FieldDesc
	sortable   []string
	selectable []string
	searchable bool
	relations  []tsRelation
	pk         string
}

// tsRelation is one entry of a resource's ?expand vocabulary, in the direction
// it is served.
type tsRelation struct {
	name     string // wire name, e.g. "list"
	target   string // TypeScript type of the expanded rows
	forward  bool   // a reference on this table, rather than one pointing at it
	nullable bool   // the reference column is nullable, so the relation may be null
}

func (r tsResource) hasExpand() bool { return len(r.relations) > 0 }

func tsResources(reg *schema.Registry) ([]tsResource, error) {
	var out []tsResource
	for _, t := range reg.Tables() {
		rest := t.Rest()
		if rest == nil {
			continue
		}
		r := tsResource{
			table:    t,
			typeName: TypeName(t.LocalName()),
			ident:    tsIdent(lowerFirst(TypeName(t.LocalName()))),
			plural:   GoName(t.LocalName()),
			path:     rest.Path,
			ops:      rest.Ops,
		}
		for _, f := range t.Fields() {
			d := f.Desc()
			if d.Hidden {
				continue
			}
			if d.PrimaryKey {
				r.pk = d.Name
			}
			r.selectable = append(r.selectable, d.Name)
			if d.Filterable {
				r.filterable = append(r.filterable, d)
			}
			if d.Sortable {
				r.sortable = append(r.sortable, d.Name)
			}
			if d.Searchable {
				r.searchable = true
			}
		}
		// The expandable set comes from the columns, exactly as the generated
		// rest.Options does, so the client cannot offer a relation the server
		// would reject or miss one it would serve.
		for _, name := range expandableRelations(reg, t) {
			rel, err := tsRelationOf(reg, t, name)
			if err != nil {
				return nil, err
			}
			r.relations = append(r.relations, rel)
		}
		out = append(out, r)
	}
	return out, nil
}

func tsRelationOf(reg *schema.Registry, t *schema.TableDef, name string) (tsRelation, error) {
	for _, f := range t.Fields() {
		d := f.Desc()
		if d.Expandable && d.Ref != nil && !d.Ref.External && d.Ref.Name == name {
			if d.Ref.Table == nil {
				return tsRelation{}, fmt.Errorf("codegen: table %s: relation %q has no target table", t.Name(), name)
			}
			return tsRelation{
				name:     name,
				target:   TypeName(d.Ref.Table.LocalName()),
				forward:  true,
				nullable: d.Nullable,
			}, nil
		}
	}
	for _, inv := range reg.Inverses(t) {
		if inv.Expandable && inv.Name == name {
			return tsRelation{name: name, target: TypeName(inv.Table.LocalName())}, nil
		}
	}
	return tsRelation{}, fmt.Errorf("codegen: table %s: no relation named %q", t.Name(), name)
}

// tsRowTypes emits the enums, the row interface and the request bodies for one
// table.
func tsRowTypes(b *bytes.Buffer, reg *schema.Registry, t *schema.TableDef) {
	typeName := TypeName(t.LocalName())
	fmt.Fprintf(b, "\n// %s\n", tsRule(t.Name()))

	for _, f := range t.Fields() {
		d := f.Desc()
		if d.Type != schema.TypeEnum || len(d.EnumValues) == 0 || d.Hidden {
			continue
		}
		fmt.Fprintf(b, "\n/** The %s.%s column's value set. */\n", t.Name(), d.Name)
		fmt.Fprintf(b, "export type %s =%s;\n", tsEnumName(typeName, d), tsUnion(d.EnumValues))
	}

	fmt.Fprintln(b)
	if c := t.Comment(); c != "" {
		fmt.Fprintf(b, "/** %s */\n", c)
	} else {
		fmt.Fprintf(b, "/** A row of %s. */\n", t.Name())
	}
	fmt.Fprintf(b, "export interface %s {\n", typeName)

	// Property names are the `json` tag spelling, which is snake_case.
	// Camel-casing would need a runtime mapping layer, and the point of the
	// emitted client is that there is nothing between the response and the
	// caller. ADR-0028.
	rels := tsForwardRelations(t)
	for _, f := range t.Fields() {
		d := f.Desc()
		if d.Hidden {
			// Absent from the row type entirely, as it is from the response
			// and from the typed column facade. A hidden column has no
			// spelling a client could use.
			continue
		}
		tsDoc(b, "  ", d.Comment)
		fmt.Fprintf(b, "  %s: %s;\n", tsProp(d.Name), tsType(typeName, d))
		if rel, ok := rels[d.Name]; ok {
			fmt.Fprintf(b, "  /** Filled in by `expand: ['%s']`, absent otherwise. */\n", rel.name)
			fmt.Fprintf(b, "  %s?: %s;\n", tsProp(rel.name), tsRelationType(rel))
		}
	}
	for _, inv := range reg.Inverses(t) {
		if !inv.Expandable {
			continue
		}
		fmt.Fprintf(b, "  /** Filled in by `expand: ['%s']`, capped at %d rows. */\n", inv.Name, inv.Cap())
		fmt.Fprintf(b, "  %s?: Collection<%s>;\n", tsProp(inv.Name), TypeName(inv.Table.LocalName()))
	}
	fmt.Fprintln(b, "}")

	tsBodyTypes(b, t, typeName)
}

// tsForwardRelations is the expandable references declared on t, keyed by the
// column they hang off.
func tsForwardRelations(t *schema.TableDef) map[string]tsRelation {
	out := map[string]tsRelation{}
	for _, f := range t.Fields() {
		d := f.Desc()
		if !d.Expandable || d.Ref == nil || d.Ref.External || d.Ref.Table == nil {
			continue
		}
		out[d.Name] = tsRelation{
			name:     d.Ref.Name,
			target:   TypeName(d.Ref.Table.LocalName()),
			forward:  true,
			nullable: d.Nullable,
		}
	}
	return out
}

// tsBodyTypes emits the create and patch bodies, over the same column sets the
// Go bodies use, so the two cannot disagree about what a request may write.
func tsBodyTypes(b *bytes.Buffer, t *schema.TableDef, typeName string) {
	rest := t.Rest()
	if rest == nil {
		return
	}

	if rest.Ops.Has(schema.OpCreate) {
		fields := bodyFields(t, forCreate)
		fmt.Fprintf(b, "\n/**\n * The request body for creating a %s.\n *\n", typeName)
		fmt.Fprint(b, " * Read-only columns are absent: the database or a BeforeCreate hook owns\n")
		fmt.Fprint(b, " * them. A column with a default is optional.\n */\n")
		fmt.Fprintf(b, "export interface %sCreate {\n", typeName)
		for _, f := range fields {
			d := f.Desc()
			tsDoc(b, "  ", d.Comment)
			fmt.Fprintf(b, "  %s%s: %s;\n", tsProp(d.Name), tsOptional(optionalOnCreate(d)), tsType(typeName, d))
		}
		fmt.Fprintln(b, "}")
	}

	if rest.Ops.Has(schema.OpUpdate) {
		fields := bodyFields(t, forUpdate)
		if len(fields) == 0 {
			return
		}
		fmt.Fprintf(b, "\n/**\n * The request body for patching a %s.\n *\n", typeName)
		fmt.Fprint(b, " * Every property is optional, so a request writes only the columns it\n")
		fmt.Fprint(b, " * names. Immutable columns are absent: they are settable once, at create.\n")
		fmt.Fprint(b, " * Sending `null` for a nullable column writes NULL; omitting it changes\n")
		fmt.Fprint(b, " * nothing, which is the distinction the server reads off the JSON body.\n */\n")
		fmt.Fprintf(b, "export interface %sPatch {\n", typeName)
		for _, f := range fields {
			d := f.Desc()
			tsDoc(b, "  ", d.Comment)
			fmt.Fprintf(b, "  %s?: %s;\n", tsProp(d.Name), tsType(typeName, d))
		}
		fmt.Fprintln(b, "}")
	}
}

// tsResourceSection emits the query vocabulary, the transport functions and the
// key factory for one exposed resource.
func tsResourceSection(b *bytes.Buffer, r tsResource) {
	fmt.Fprintf(b, "\n// %s\n", tsRule(r.path))

	// Layer 2: the vocabulary. Each of these is a closed union, so a column
	// that did not opt in to a capability has no spelling that compiles.
	fmt.Fprintf(b, "\n/** Columns `select` may name. The primary key is always returned. */\n")
	fmt.Fprintf(b, "export type %sColumn =%s;\n", r.typeName, tsUnion(r.selectable))

	if len(r.sortable) > 0 {
		terms := make([]string, 0, len(r.sortable)*2)
		for _, name := range r.sortable {
			terms = append(terms, name, "-"+name)
		}
		fmt.Fprintf(b, "\n/** Sortable columns, with their descending forms. */\n")
		fmt.Fprintf(b, "export type %sSort =%s;\n", r.typeName, tsUnion(terms))
	}

	if r.hasExpand() {
		names := make([]string, len(r.relations))
		for i, rel := range r.relations {
			names[i] = rel.name
		}
		fmt.Fprintf(b, "\n/** Relations `expand` may name. */\n")
		fmt.Fprintf(b, "export type %sExpand =%s;\n", r.typeName, tsUnion(names))
	}

	fmt.Fprintf(b, "\n/**\n * Filter conditions, one property per filterable column.\n *\n")
	fmt.Fprint(b, " * A bare value is equality; an object names operators. The operator set is\n")
	fmt.Fprint(b, " * narrowed by column type, so a pattern match against a number and a null\n")
	fmt.Fprint(b, " * test against a non-nullable column do not compile.\n */\n")
	// A type alias rather than an interface, so that it satisfies the encoder's
	// Record<string, unknown>: TypeScript gives an object type alias an
	// implicit index signature and an interface none.
	fmt.Fprintf(b, "export type %sWhere = {\n", r.typeName)
	for _, d := range r.filterable {
		fmt.Fprintf(b, "  %s?: %s;\n", tsProp(d.Name), tsCondType(r.typeName, d))
	}
	fmt.Fprintln(b, "};")

	tsParamTypes(b, r)
	tsRowType(b, r)
	tsTransport(b, r)
	tsKeys(b, r)
}

func tsParamTypes(b *bytes.Buffer, r tsResource) {
	fmt.Fprintf(b, "\n/** Parameters for `GET %s`. */\n", r.path)
	fmt.Fprintf(b, "export interface %sListParams%s {\n", r.typeName, tsNarrowingParams(r))
	fmt.Fprintf(b, "  where?: %sWhere;\n", r.typeName)
	if r.searchable {
		fmt.Fprint(b, "  /** Case-insensitive substring match, fanned out over the searchable columns. */\n")
		fmt.Fprint(b, "  search?: string;\n")
	}
	if len(r.sortable) > 0 {
		fmt.Fprintf(b, "  /** Ordering, most significant first. */\n  sort?: %sSort | readonly %sSort[];\n", r.typeName, r.typeName)
	}
	fmt.Fprint(b, "  /** Columns to return. Omitted columns are absent from the response, and the\n")
	fmt.Fprint(b, "   * response type narrows to match. */\n")
	fmt.Fprint(b, "  select?: readonly S[];\n")
	if r.hasExpand() {
		fmt.Fprint(b, "  expand?: readonly E[];\n")
	}
	fmt.Fprint(b, "  page?: number;\n  per_page?: number;\n")
	if r.pk != "" {
		fmt.Fprint(b, "  /** Resume after a `next_cursor` from a previous response. Cannot be combined\n")
		fmt.Fprint(b, "   * with `page`, and is only valid for the `sort` it was issued under. */\n")
		fmt.Fprint(b, "  cursor?: string;\n")
	}
	fmt.Fprint(b, "  /** Ask for a total row count, which costs a second query. */\n  count?: 'exact';\n")
	fmt.Fprint(b, "  /** Parameters this vocabulary cannot express, appended verbatim. Reaching for\n")
	fmt.Fprint(b, "   * it often means the typed layer is in the wrong place — ADR-0028 says so and\n")
	fmt.Fprint(b, "   * names it as the signal to watch. */\n")
	fmt.Fprint(b, "  params?: Record<string, string | readonly string[]>;\n")
	fmt.Fprintln(b, "}")

	if r.ops.Has(schema.OpRead) {
		fmt.Fprintf(b, "\n/**\n * Parameters for `GET %s/{id}`.\n *\n", r.path)
		fmt.Fprint(b, " * There is no `select` here: the item endpoint rejects unknown query\n")
		fmt.Fprint(b, " * parameters and does not declare one.\n */\n")
		if r.hasExpand() {
			fmt.Fprintf(b, "export interface %sGetParams<E extends %sExpand = never> {\n", r.typeName, r.typeName)
			fmt.Fprint(b, "  expand?: readonly E[];\n}\n")
		} else {
			fmt.Fprintf(b, "export type %sGetParams = Record<string, never>;\n", r.typeName)
		}
	}
}

// tsNarrowingParams is the generic parameter list a params type carries: the
// projection, and the expansion where there is one.
func tsNarrowingParams(r tsResource) string {
	if r.hasExpand() {
		return fmt.Sprintf("<S extends %sColumn = %sColumn, E extends %sExpand = never>",
			r.typeName, r.typeName, r.typeName)
	}
	return fmt.Sprintf("<S extends %sColumn = %sColumn>", r.typeName, r.typeName)
}

func tsNarrowingArgs(r tsResource, s, e string) string {
	if r.hasExpand() {
		return "<" + s + ", " + e + ">"
	}
	return "<" + s + ">"
}

// tsRowType emits the response type, narrowed by the projection and widened by
// whatever was expanded.
func tsRowType(b *bytes.Buffer, r tsResource) {
	fmt.Fprintf(b, "\n/**\n * A %s as one request asked for it: the selected columns, plus the relations\n", r.typeName)
	fmt.Fprint(b, " * it expanded.\n */\n")
	fmt.Fprintf(b, "export type %sRow%s =\n", r.typeName, tsNarrowingParams(r))

	pick := fmt.Sprintf("Pick<%s, S>", r.typeName)
	if r.pk != "" {
		// The server adds the primary key back to any projection that dropped
		// it, since a row that cannot address itself is of little use. The type
		// says the same.
		pick = fmt.Sprintf("Pick<%s, S | %s>", r.typeName, tsString(r.pk))
	}
	fmt.Fprintf(b, "  %s", pick)
	for _, rel := range r.relations {
		fmt.Fprintf(b, "\n  & (%s extends E ? { %s: %s } : unknown)",
			tsString(rel.name), tsProp(rel.name), tsRelationType(rel))
	}
	fmt.Fprintln(b, ";")
}

func tsTransport(b *bytes.Buffer, r tsResource) {
	name := r.typeName

	if r.ops.Has(schema.OpList) {
		fmt.Fprintf(b, "\n/** `GET %s` — the filtered, sorted, paged collection. */\n", r.path)
		fmt.Fprintf(b, "export function list%s%s(\n", r.plural, tsNarrowingParams(r))
		fmt.Fprint(b, "  request: Transport,\n")
		fmt.Fprintf(b, "  params: %sListParams%s = {},\n", name, tsNarrowingArgs(r, "S", "E"))
		fmt.Fprint(b, "  signal?: AbortSignal,\n")
		fmt.Fprintf(b, "): Promise<Page<%sRow%s>> {\n", name, tsNarrowingArgs(r, "S", "E"))
		fmt.Fprintf(b, "  return request({ method: 'GET', path: %s, query: encodeListQuery(params), signal });\n}\n", tsString(r.path))
	}

	if r.ops.Has(schema.OpRead) {
		generic, args, params := "", "", ""
		if r.hasExpand() {
			generic = fmt.Sprintf("<E extends %sExpand = never>", name)
			args = fmt.Sprintf("<%sColumn, E>", name)
			params = fmt.Sprintf("  params: %sGetParams<E> = {},\n", name)
		} else {
			args = fmt.Sprintf("<%sColumn>", name)
			params = fmt.Sprintf("  params: %sGetParams = {},\n", name)
		}
		fmt.Fprintf(b, "\n/** `GET %s/{id}` — one row by primary key. */\n", r.path)
		fmt.Fprintf(b, "export function get%s%s(\n", name, generic)
		fmt.Fprint(b, "  request: Transport,\n  id: string | number,\n")
		fmt.Fprint(b, params)
		fmt.Fprint(b, "  signal?: AbortSignal,\n")
		fmt.Fprintf(b, "): Promise<%sRow%s> {\n", name, args)
		fmt.Fprintf(b, "  return request({ method: 'GET', path: itemPath(%s, id), query: encodeItemQuery(params), signal });\n}\n", tsString(r.path))
	}

	if r.ops.Has(schema.OpCreate) {
		fmt.Fprintf(b, "\n/** `POST %s` — create a row. */\n", r.path)
		fmt.Fprintf(b, "export function create%s(request: Transport, body: %sCreate, signal?: AbortSignal): Promise<%s> {\n",
			name, name, name)
		fmt.Fprintf(b, "  return request({ method: 'POST', path: %s, body, signal });\n}\n", tsString(r.path))
	}

	if r.ops.Has(schema.OpUpdate) && len(bodyFields(r.table, forUpdate)) > 0 {
		fmt.Fprintf(b, "\n/** `PATCH %s/{id}` — write the columns the body names, and no others. */\n", r.path)
		fmt.Fprintf(b, "export function update%s(request: Transport, id: string | number, body: %sPatch, signal?: AbortSignal): Promise<%s> {\n",
			name, name, name)
		fmt.Fprintf(b, "  return request({ method: 'PATCH', path: itemPath(%s, id), body, signal });\n}\n", tsString(r.path))
	}

	if r.ops.Has(schema.OpDelete) {
		fmt.Fprintf(b, "\n/** `DELETE %s/{id}`. */\n", r.path)
		fmt.Fprintf(b, "export function delete%s(request: Transport, id: string | number, signal?: AbortSignal): Promise<void> {\n", name)
		fmt.Fprintf(b, "  return request({ method: 'DELETE', path: itemPath(%s, id), signal });\n}\n", tsString(r.path))
	}
}

// tsKeys emits the query-key factory.
//
// It is here rather than in the TanStack file because a key is a plain array
// and a change-feed subscriber needs one without needing a query client. The
// reason it exists at all is that two hand-written lists of invalidation keys
// drift, and one list cannot.
func tsKeys(b *bytes.Buffer, r tsResource) {
	fmt.Fprintf(b, "\n/**\n * Cache keys for %s. Deriving them is what keeps a mutation's invalidation\n", r.path)
	fmt.Fprint(b, " * and a change-feed subscriber's invalidation from being two lists that can\n")
	fmt.Fprint(b, " * disagree.\n */\n")
	fmt.Fprintf(b, "export const %sKeys = {\n", r.ident)
	fmt.Fprintf(b, "  all: () => [%s] as const,\n", tsString(r.table.Name()))
	fmt.Fprintf(b, "  lists: () => [%s, 'list'] as const,\n", tsString(r.table.Name()))
	fmt.Fprintf(b, "  list: (params: unknown = {}) => [%s, 'list', params] as const,\n", tsString(r.table.Name()))
	fmt.Fprintf(b, "  infinite: (params: unknown = {}) => [%s, 'infinite', params] as const,\n", tsString(r.table.Name()))
	fmt.Fprintf(b, "  details: () => [%s, 'detail'] as const,\n", tsString(r.table.Name()))
	fmt.Fprintf(b, "  detail: (id: string | number, params: unknown = {}) => [%s, 'detail', String(id), params] as const,\n", tsString(r.table.Name()))
	fmt.Fprintln(b, "};")
}

// tsKeyIndex maps a table name onto its key factory, which is what makes a
// change-feed event mechanical: the event carries a table and a row key
// (ADR-0012), and this is the derivation that turns those into the keys to
// invalidate.
func tsKeyIndex(b *bytes.Buffer, resources []tsResource) {
	if len(resources) == 0 {
		return
	}
	fmt.Fprintf(b, "\n// %s\n", tsRule("change feed"))
	fmt.Fprint(b, "\n/**\n * Key factories by table name, for a subscriber that receives a table and a\n")
	fmt.Fprint(b, " * row key and has to decide what to refetch.\n */\n")
	fmt.Fprint(b, "export const keysByTable = {\n")
	for _, r := range resources {
		fmt.Fprintf(b, "  %s: %sKeys,\n", tsProp(r.table.Name()), r.ident)
	}
	fmt.Fprint(b, "} as const;\n")
	fmt.Fprint(b, "\nexport type TableName = keyof typeof keysByTable;\n")
}

// tsQueriesSection emits the TanStack factories for one resource.
func tsQueriesSection(b *bytes.Buffer, r tsResource) {
	name := r.typeName
	fmt.Fprintf(b, "\n// %s\n", tsRule(r.path))
	fmt.Fprintf(b, "\n/**\n * Read options for %s, bound to a transport.\n *\n", r.path)
	fmt.Fprint(b, " * `queryOptions` objects rather than hooks: an options object is spread and\n")
	fmt.Fprint(b, " * overridden — `{ ...queries.list(p), staleTime: 30_000 }` — where a hook is\n")
	fmt.Fprint(b, " * copied out and edited, which is the signal that a seam is in the wrong\n")
	fmt.Fprint(b, " * place.\n */\n")
	fmt.Fprintf(b, "export function %sQueries(request: Transport) {\n", r.ident)
	fmt.Fprint(b, "  return {\n")

	if r.ops.Has(schema.OpList) {
		fmt.Fprintf(b, "    list: %s(params: %sListParams%s = {}) =>\n",
			tsNarrowingParams(r), name, tsNarrowingArgs(r, "S", "E"))
		fmt.Fprint(b, "      queryOptions({\n")
		fmt.Fprintf(b, "        queryKey: %sKeys.list(params),\n", r.ident)
		fmt.Fprintf(b, "        queryFn: ({ signal }) => list%s(request, params, signal),\n", r.plural)
		fmt.Fprint(b, "      }),\n")

		if r.pk != "" {
			// Cursor paging is what infiniteQueryOptions already wants:
			// getNextPageParam returns next_cursor, and undefined when the
			// response omits it. `page` and `cursor` are two answers to where a
			// page starts, so the params type here has neither — the factory
			// owns the paging.
			fmt.Fprintf(b, "    infinite: %s(params: Omit<%sListParams%s, 'page' | 'cursor'> = {}) =>\n",
				tsNarrowingParams(r), name, tsNarrowingArgs(r, "S", "E"))
			fmt.Fprint(b, "      infiniteQueryOptions({\n")
			fmt.Fprintf(b, "        queryKey: %sKeys.infinite(params),\n", r.ident)
			fmt.Fprintf(b, "        queryFn: ({ pageParam, signal }) => list%s(request, { ...params, cursor: pageParam }, signal),\n", r.plural)
			fmt.Fprint(b, "        initialPageParam: undefined as string | undefined,\n")
			fmt.Fprint(b, "        getNextPageParam: (last) => last.next_cursor,\n")
			fmt.Fprint(b, "      }),\n")
		}
	}

	if r.ops.Has(schema.OpRead) {
		if r.hasExpand() {
			fmt.Fprintf(b, "    detail: <E extends %sExpand = never>(id: string | number, params: %sGetParams<E> = {}) =>\n", name, name)
		} else {
			fmt.Fprintf(b, "    detail: (id: string | number, params: %sGetParams = {}) =>\n", name)
		}
		fmt.Fprint(b, "      queryOptions({\n")
		fmt.Fprintf(b, "        queryKey: %sKeys.detail(id, params),\n", r.ident)
		fmt.Fprintf(b, "        queryFn: ({ signal }) => get%s(request, id, params, signal),\n", name)
		fmt.Fprint(b, "      }),\n")
	}

	fmt.Fprint(b, "  };\n}\n")
}

// tsType is the TypeScript type of a column in a row or a request body.
func tsType(typeName string, d *schema.FieldDesc) string {
	base := tsBaseType(typeName, d)
	if d.Nullable {
		return base + " | null"
	}
	return base
}

func tsBaseType(typeName string, d *schema.FieldDesc) string {
	if d.Type == schema.TypeEnum && len(d.EnumValues) > 0 {
		return tsEnumName(typeName, d)
	}
	switch d.Type {
	case schema.TypeInt, schema.TypeBigInt, schema.TypeFloat, schema.TypeNumeric:
		return "number"
	case schema.TypeBool:
		return "boolean"
	case schema.TypeJSON:
		return "unknown"
	default:
		// Text, varchar, uuid, bytea and the three time types all arrive as
		// strings. A timestamp is RFC 3339; typing it as Date would be a lie
		// about what JSON.parse returns.
		return "string"
	}
}

// tsCondType is the filter condition a column accepts: the operator set
// narrowed by type, which is the part an OpenAPI document cannot say.
func tsCondType(typeName string, d *schema.FieldDesc) string {
	value := tsBaseType(typeName, d)
	// A timestamp is a string on the wire and a Date in most application code,
	// and the encoder accepts either, so both compile here.
	switch d.Type {
	case schema.TypeTimestamp, schema.TypeDate, schema.TypeTime:
		value += " | Date"
	}

	var extras []string
	// Pattern operators need a text column: the server refuses them on
	// anything else, and an enum is a string in SQL but compared by equality in
	// practice, so it is excluded here as it is in the typed facade.
	if d.Type == schema.TypeText || d.Type == schema.TypeVarchar {
		extras = append(extras, "TextMatch")
	}
	if d.Nullable {
		extras = append(extras, "NullCheck")
	}
	if len(extras) == 0 {
		return fmt.Sprintf("Cond<%s>", value)
	}
	return fmt.Sprintf("Cond<%s, %s>", value, strings.Join(extras, " & "))
}

func tsRelationType(rel tsRelation) string {
	if !rel.forward {
		return "Collection<" + rel.target + ">"
	}
	if rel.nullable {
		return rel.target + " | null"
	}
	return rel.target
}

func tsEnumName(typeName string, d *schema.FieldDesc) string {
	return typeName + GoName(d.Name)
}

// tsProp quotes a property name that is not a plain identifier. Column names
// are snake_case and almost never need it; a name that does would otherwise
// emit invalid TypeScript.
func tsProp(name string) string {
	if isTSIdent(name) {
		return name
	}
	return tsString(name)
}

func isTSIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || r == '$':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// tsIdent avoids emitting a reserved word as a binding name.
func tsIdent(s string) string {
	switch s {
	case "await", "break", "case", "catch", "class", "const", "continue", "debugger",
		"default", "delete", "do", "else", "enum", "export", "extends", "false",
		"finally", "for", "function", "if", "import", "in", "instanceof", "new",
		"null", "return", "super", "switch", "this", "throw", "true", "try",
		"typeof", "var", "void", "while", "with", "yield":
		return s + "_"
	}
	return s
}

// tsUnion renders the right-hand side of a union of string literals, including
// the leading space or line break, so that a long union breaks one member per
// line and a short one does not.
func tsUnion(values []string) string {
	if len(values) == 0 {
		return " never"
	}
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = tsString(v)
	}
	if len(quoted) <= 4 {
		return " " + strings.Join(quoted, " | ")
	}
	return "\n  | " + strings.Join(quoted, "\n  | ")
}

// tsString renders a TypeScript single-quoted string literal.
func tsString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`)
	return "'" + r.Replace(s) + "'"
}

func tsOptional(optional bool) string {
	if optional {
		return "?"
	}
	return ""
}

// tsDoc emits a doc comment for a column, when the schema wrote one.
func tsDoc(b *bytes.Buffer, indent, comment string) {
	if comment == "" {
		return
	}
	fmt.Fprintf(b, "%s/** %s */\n", indent, comment)
}

// tsRule is a section divider, padded so the generated file has visible seams.
func tsRule(label string) string {
	const width = 72
	rule := strings.Repeat("-", max(3, width-len(label)-1))
	return rule + " " + label
}

func uniqueSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

const tsClientHeader = `// Code generated by github.com/jryannel/sqlb. DO NOT EDIT.
//
// The typed client for this schema: row types, request parameters, the encoder
// for the filter grammar, transport functions and cache keys.
//
// It imports nothing. The transport is injected, because session storage,
// token refresh and redirect-on-401 are the application's and are not
// derivable from a schema — the same seam the server takes by mounting onto a
// router the application built.
//
// Property names are the wire spelling, which is snake_case. Camel-casing
// would need a runtime mapping layer between the response and the caller, and
// the point of a generated client is that there is nothing there.
//
// ADR-0028.
`

// tsRuntime is the part of the emitted file that does not depend on the
// schema: the response envelopes, the operator vocabulary, the transport
// interface and the URL encoder.
//
// It is inlined rather than imported from a published package, for the reason
// the models are: a client generated against the server it talks to cannot be a
// version behind it.
const tsRuntime = `
// ------------------------------------------------------------------- runtime

/** A capped set of expanded child rows. ` + "`has_more`" + ` reports truncation, which a
 * bare array could not, so a caller reading twenty of two hundred can tell. */
export interface Collection<T> {
  items: T[];
  has_more: boolean;
}

/** The body of every list response: a collection, plus where in the walk it is.
 *
 * It extends Collection rather than restating it, because an expansion returns
 * a strict subset of these fields and two hand-written shapes would be free to
 * drift. */
export interface Page<T> extends Collection<T> {
  page: number;
  per_page: number;
  /** The position to resume from, present whenever a next page exists. Prefer it
   * to ` + "`page`" + `: it costs the same at any depth and cannot skip or repeat a row
   * when the table is written to mid-walk. */
  next_cursor?: string;
  /** Total matching rows, present only when ` + "`count: 'exact'`" + ` was asked for. */
  total?: number;
}

/** One rejected parameter or field. */
export interface ProblemDetail {
  message: string;
  /** Where the problem is, e.g. ` + "`query.sort`" + `. */
  location?: string;
  value?: unknown;
  /** What would have been accepted instead, where the set is finite. This is the
   * half of an error that turns a dead end into a fix, and it is why the client
   * carries an error type at all. */
  allowed?: string[];
}

/** The RFC 9457 problem document every rejection returns. */
export interface Problem {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  errors?: ProblemDetail[];
}

/** Narrows an unknown error body to a Problem. */
export function isProblem(value: unknown): value is Problem {
  if (typeof value !== 'object' || value === null) return false;
  const p = value as Problem;
  return typeof p.status === 'number' || Array.isArray(p.errors);
}

/** The allowed values a rejection named for one parameter, e.g.
 * ` + "`allowedFor(problem, 'query.sort')`" + `. */
export function allowedFor(problem: Problem, location: string): string[] {
  return problem.errors?.find((e) => e.location === location)?.allowed ?? [];
}

/** One request, as the generated functions describe it. */
export interface ApiRequest {
  method: 'GET' | 'POST' | 'PATCH' | 'DELETE';
  /** Path from the API root, already encoded, e.g. ` + "`/tasks/1`" + `. */
  path: string;
  /** Encoded query string without the leading ` + "`?`" + `. */
  query?: string;
  body?: unknown;
  signal?: AbortSignal;
}

/**
 * The application's request function.
 *
 * Everything not derivable from the schema lives behind this: the base URL,
 * the auth header, refresh, retry, and what a 401 does. A minimal one:
 *
 *     const request: Transport = async ({ method, path, query, body, signal }) => {
 *       const res = await fetch(` + "`${base}${path}${query ? '?' + query : ''}`" + `, {
 *         method,
 *         headers: { ...(body ? { 'content-type': 'application/json' } : {}), ...auth() },
 *         body: body === undefined ? undefined : JSON.stringify(body),
 *         signal,
 *       });
 *       if (!res.ok) throw await res.json();
 *       return res.status === 204 ? (undefined as never) : res.json();
 *     };
 */
export type Transport = <T>(request: ApiRequest) => Promise<T>;

/** Operators every column type accepts. */
export interface Comparison<V> {
  eq?: V;
  ne?: V;
  gt?: V;
  gte?: V;
  lt?: V;
  lte?: V;
  in?: readonly V[];
  nin?: readonly V[];
  between?: readonly [V, V];
}

/** Null tests, offered only by nullable columns. */
export interface NullCheck {
  isnull?: boolean;
  notnull?: boolean;
}

/** Pattern operators, offered only by text columns. */
export interface TextMatch {
  like?: string;
  ilike?: string;
  /** Case-insensitive substring. The value is escaped, so ` + "`50%`" + ` matches that
   * literal string rather than everything. */
  contains?: string;
  startswith?: string;
  endswith?: string;
}

/** One column's filter: a bare value for equality, or an operator object. */
export type Cond<V, Extra = unknown> = V | (Comparison<V> & Extra);

type Scalar = string | number | boolean | Date;

function encodeScalar(v: Scalar): string {
  return v instanceof Date ? v.toISOString() : String(v);
}

/** A member of a comma-separated list is quoted when it carries a comma or a
 * quote, which is how the server's parser reads it back whole. */
function encodeMember(v: Scalar): string {
  const s = encodeScalar(v);
  return /[,"]/.test(s) ? '"' + s.replace(/"/g, '\\"') + '"' : s;
}

function appendCond(out: URLSearchParams, column: string, cond: unknown): void {
  if (cond === undefined) return;
  // A bare null is read as a null test rather than as an equality against NULL,
  // which is not a comparison SQL would answer true to anyway.
  if (cond === null) {
    out.append(column, 'isnull');
    return;
  }
  if (typeof cond !== 'object' || cond instanceof Date) {
    out.append(column, 'eq.' + encodeScalar(cond as Scalar));
    return;
  }
  // Repeating a parameter conjoins its conditions, so an object with two
  // operators becomes two parameters rather than one compound value.
  for (const [op, value] of Object.entries(cond as Record<string, unknown>)) {
    if (value === undefined || value === null) continue;
    switch (op) {
      case 'isnull':
      case 'notnull':
        if (value) out.append(column, op);
        break;
      case 'in':
      case 'nin':
        out.append(column, op + '.' + (value as Scalar[]).map(encodeMember).join(','));
        break;
      case 'between': {
        const [lo, hi] = value as [Scalar, Scalar];
        out.append(column, 'between.' + encodeMember(lo) + ',' + encodeMember(hi));
        break;
      }
      default:
        out.append(column, op + '.' + encodeScalar(value as Scalar));
    }
  }
}

/** The shape encodeListQuery reads. Each resource's params type is one of
 * these with its columns and operators pinned. */
export interface ListQuery {
  where?: Record<string, unknown>;
  search?: string;
  sort?: string | readonly string[];
  select?: readonly string[];
  expand?: readonly string[];
  page?: number;
  per_page?: number;
  cursor?: string;
  count?: 'exact';
  params?: Record<string, string | readonly string[]>;
}

/**
 * Encodes list parameters into the server's query grammar.
 *
 * This is the piece hand-written clients open-code and get subtly wrong: the
 * operator prefixes, the repeated parameters that conjoin, and the quoting
 * inside a value list.
 */
export function encodeListQuery(query: ListQuery = {}): string {
  const out = new URLSearchParams();
  for (const [column, cond] of Object.entries(query.where ?? {})) appendCond(out, column, cond);
  if (query.search) out.set('search', query.search);
  if (query.sort) out.set('sort', typeof query.sort === 'string' ? query.sort : query.sort.join(','));
  if (query.select?.length) out.set('select', query.select.join(','));
  if (query.expand?.length) out.set('expand', query.expand.join(','));
  if (query.page !== undefined) out.set('page', String(query.page));
  if (query.per_page !== undefined) out.set('per_page', String(query.per_page));
  if (query.cursor !== undefined) out.set('cursor', query.cursor);
  if (query.count !== undefined) out.set('count', query.count);
  for (const [key, value] of Object.entries(query.params ?? {})) {
    for (const item of Array.isArray(value) ? value : [value as string]) out.append(key, item);
  }
  // Sorted, so that the same parameters always produce the same string — which
  // is what makes a URL comparable in a test and cacheable by a proxy.
  out.sort();
  return out.toString();
}

/** The item endpoint declares no parameters but ` + "`expand`" + `, and rejects any
 * other, so this encoder is deliberately not the list one. */
export function encodeItemQuery(query: { expand?: readonly string[] } = {}): string {
  const out = new URLSearchParams();
  if (query.expand?.length) out.set('expand', query.expand.join(','));
  return out.toString();
}

function itemPath(collection: string, id: string | number): string {
  return collection + '/' + encodeURIComponent(String(id));
}
`

const tsQueriesHeader = `// Code generated by github.com/jryannel/sqlb. DO NOT EDIT.
//
// TanStack Query option factories, one per exposed resource.
//
// queryOptions objects rather than hooks: a hook bakes in a framework and is
// the thing people copy out and edit, where an options object is spread and
// overridden. Deleting this file breaks only the call sites that used it — the
// types, the encoder and the keys next door do not depend on it.
//
// ADR-0028.

import { infiniteQueryOptions, queryOptions } from '@tanstack/react-query';
`
