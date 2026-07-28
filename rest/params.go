package rest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/filter"
)

// listParams builds the OpenAPI parameters for a list operation from the
// model's capabilities.
//
// This is the part ADR-0007 called the hardest problem in the generator: a
// compositional grammar like `?age=gte.18&age=lt.65` has no fixed parameter
// set. It becomes describable by enumerating one parameter per *column* — the
// columns are finite and known — and documenting the operator vocabulary that
// column's type accepts. `sort` and `select` are comma-separated arrays whose
// items enumerate the capable columns, so a client that asks for a column which
// is not sortable is wrong at its own compile step rather than at runtime.
//
// Huma merges these with anything derived from the input struct, keeping the
// parameter already present under a given name, so these definitions win.
func listParams[T any](b *binding[T]) []*huma.Param {
	var params []*huma.Param

	for _, col := range b.model.Columns {
		if col.Hidden || !col.Filterable {
			continue
		}
		params = append(params, &huma.Param{
			Name:        col.Name,
			In:          "query",
			Description: filterDescription(col),
			// Repeating a parameter conjoins its conditions, so the parameter
			// is an array. Exploded form is the repeated spelling.
			Explode: ptr(true),
			Schema: &huma.Schema{
				Type:  "array",
				Items: &huma.Schema{Type: "string"},
			},
		})
	}

	if sortable := capable(b.model, func(c *sqlb.ColumnInfo) bool { return c.Sortable }); len(sortable) > 0 {
		terms := make([]any, 0, len(sortable)*2)
		for _, name := range sortable {
			terms = append(terms, name, "-"+name)
		}
		params = append(params, &huma.Param{
			Name: "sort",
			In:   "query",
			Description: fmt.Sprintf(
				"Ordering, most significant first. Prefix a column with `-` for descending. At most %d terms.",
				orDefault(b.opts.MaxSortTerms, filter.MaxSortTerms)),
			Schema: &huma.Schema{
				Type:     "array",
				Items:    &huma.Schema{Type: "string", Enum: terms},
				MaxItems: ptr(orDefault(b.opts.MaxSortTerms, filter.MaxSortTerms)),
			},
			// Comma-separated in a single parameter, not repeated.
			Explode: ptr(false),
		})
	}

	selectable := make([]any, 0, len(b.selectable))
	for _, col := range b.selectable {
		selectable = append(selectable, col.Name)
	}
	params = append(params, &huma.Param{
		Name: "select",
		In:   "query",
		Description: "Columns to return. The primary key is always included, since a row that " +
			"cannot address itself is of little use. Omitted columns are absent from the " +
			"response object rather than present and empty.",
		Explode: ptr(false),
		Schema: &huma.Schema{
			Type:  "array",
			Items: &huma.Schema{Type: "string", Enum: selectable},
		},
	})

	if !b.opts.DisableSearch {
		if cols := capable(b.model, func(c *sqlb.ColumnInfo) bool { return c.Searchable }); len(cols) > 0 {
			params = append(params, &huma.Param{
				Name: "search",
				In:   "query",
				Description: "Case-insensitive substring match, fanned out across " +
					strings.Join(cols, ", ") + ".",
				Schema: &huma.Schema{Type: "string"},
			})
		}
	}

	if p := expandParam(b); p != nil {
		params = append(params, p)
	}

	groupDoc := "Disjunction of conditions, each written `column.operator.value`, " +
		"e.g. `(status.eq.draft,view_count.lt.10)`. Groups may nest up to " +
		fmt.Sprint(filter.MaxGroupDepth) + " levels."
	params = append(params,
		&huma.Param{
			Name: "or", In: "query", Explode: ptr(true),
			Description: groupDoc,
			Schema:      &huma.Schema{Type: "array", Items: &huma.Schema{Type: "string"}},
		},
		&huma.Param{
			Name: "and", In: "query", Explode: ptr(true),
			Description: strings.Replace(groupDoc, "Disjunction", "Conjunction", 1),
			Schema:      &huma.Schema{Type: "array", Items: &huma.Schema{Type: "string"}},
		},
	)

	maxPage := orDefault(b.opts.MaxPageSize, filter.MaxPageSize)
	defaultPage := orDefault(b.opts.DefaultPageSize, filter.DefaultPageSize)
	params = append(params,
		&huma.Param{
			Name: "page", In: "query",
			Description: "Page number, 1-based.",
			Schema:      &huma.Schema{Type: "integer", Minimum: fptr(1), Default: 1},
		},
		&huma.Param{
			Name: "per_page", In: "query",
			Description: fmt.Sprintf(
				"Rows per page. Values above %d are capped at %d rather than rejected.", maxPage, maxPage),
			Schema: &huma.Schema{Type: "integer", Minimum: fptr(1), Default: defaultPage},
		},
		&huma.Param{
			Name: "limit", In: "query",
			Description: "Alternative spelling of per_page.",
			Schema:      &huma.Schema{Type: "integer", Minimum: fptr(1)},
		},
		&huma.Param{
			Name: "offset", In: "query",
			Description: "Rows to skip. Ignored when page is given.",
			Schema:      &huma.Schema{Type: "integer", Minimum: fptr(0)},
		},
		&huma.Param{
			Name: "count", In: "query",
			Description: "Set to `exact` to include the total row count. It costs a second " +
				"query against the same filter, so it is opt-in; `has_more` is always present " +
				"and is enough to drive paging.",
			Schema: &huma.Schema{Type: "string", Enum: []any{"exact"}},
		},
	)

	return params
}

// expandParam documents `?expand`, or returns nil when the resource declares no
// expandable relation.
//
// Shared by the list and the item operations rather than written twice: the
// enum is the contract a client generates against, and two copies of it is two
// places for a relation to be missing from.
//
// Returning nil rather than an empty enum is what makes the parameter unknown
// on a resource with no relations, which — with RejectUnknownQueryParameters on
// the item operation — is the difference between refusing `?expand=author` and
// accepting it and answering without it.
func expandParam[T any](b *binding[T]) *huma.Param {
	if len(b.opts.Expandable) == 0 {
		return nil
	}
	relations := make([]any, len(b.opts.Expandable))
	for i, name := range b.opts.Expandable {
		relations[i] = name
	}
	return &huma.Param{
		Name:        "expand",
		In:          "query",
		Description: "Relations to embed.",
		Explode:     ptr(false),
		Schema: &huma.Schema{
			Type:  "array",
			Items: &huma.Schema{Type: "string", Enum: relations},
		},
	}
}

// filterDescription documents the operators a column accepts. The pattern
// operators need a text column, so offering them on an integer would document a
// request that parsing rejects.
func filterDescription(col *sqlb.ColumnInfo) string {
	// A document column takes containment and nothing else: it has no
	// shorthand, and the ordering and pattern operators are refused. Listing
	// the general set here would document a request that parsing rejects,
	// which is the same mistake as offering `startswith` on an integer.
	if isJSON(col.Type) {
		ops := []string{"contains"}
		if col.Nullable {
			ops = append(ops, "isnull", "notnull")
		}
		return fmt.Sprintf(
			"Filter on `%s`, a JSON document. Written `contains.{…}`, which matches rows "+
				"whose document contains the one given — `contains.{\"lang\":\"de\"}` matches "+
				"any document carrying that key and value, whatever else it holds. There is no "+
				"bare-value shorthand. Operators: %s.",
			col.Name, strings.Join(ops, ", "))
	}

	ops := []string{"eq", "ne", "gt", "gte", "lt", "lte", "in", "nin", "between"}
	if col.Nullable {
		ops = append(ops, "isnull", "notnull")
	}
	if isText(col.Type) {
		ops = append(ops, "like", "ilike", "contains", "startswith", "endswith")
	}
	return fmt.Sprintf(
		"Filter on `%s`. Written `operator.value`, or a bare value for equality. "+
			"Repeat the parameter to conjoin conditions. Operators: %s.",
		col.Name, strings.Join(ops, ", "))
}

func isText(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.String
}

var jsonRawMessageType = reflect.TypeOf(json.RawMessage(nil))

// isJSON reports whether a column holds a jsonb document. It matches the named
// type rather than the kind, because json.RawMessage and the []byte a bytea
// column maps to are both slices of bytes and only one is a document. The
// filter package makes the same distinction for the same reason; it is
// duplicated rather than exported, as isText already is.
func isJSON(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t == jsonRawMessageType
}

// capable lists the non-hidden columns satisfying want, for documentation.
func capable(m *sqlb.Model, want func(*sqlb.ColumnInfo) bool) []string {
	var out []string
	for _, col := range m.Columns {
		if !col.Hidden && want(col) {
			out = append(out, col.Name)
		}
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func fptr(v float64) *float64 { return &v }

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}
