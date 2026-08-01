package rest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jryannel/sqlb"
)

// binding is everything about one resource that can be computed once, at
// registration, rather than per request: the model, the JSON name of each
// column, and the columns a request body may write.
type binding[T any] struct {
	opts  Options
	model *sqlb.Model

	// jsonName maps a column name to the JSON property it serialises as. The
	// two usually coincide, because codegen writes `json:"org_id"` beside
	// `db:"org_id"`, but a hand-written model may disagree and the response
	// must follow the struct, not the column.
	jsonName map[string]string

	// selectable is the default projection: every non-hidden column, minus the
	// computed ones this resource did not opt into. It drives the response
	// body's keys as well as the SELECT list, so a computed column the mount
	// does not select is absent from the JSON rather than present holding its
	// zero value — which for a bool would have been indistinguishable from a
	// real false (#92).
	selectable []*sqlb.ColumnInfo

	// writable is what a request body may set. Read-only columns are excluded
	// because the database or a hook owns them, and hidden ones because a
	// column that never leaves the process should not be settable from
	// outside it either.
	writable []*sqlb.ColumnInfo

	// readOnly is the complement of writable, as reflect field paths.
	//
	// Paths rather than column names because of how the guarantee is enforced:
	// the fields are cleared on the row a request produced, rather than the
	// columns being omitted from the INSERT. See clearReadOnly.
	readOnly [][]int
}

// clearReadOnly resets every read-only field of value to its zero value.
//
// This is what stops a request writing a column the schema says it may not.
// The generated create body has no field for one, so ordinarily there is
// nothing to clear — but a hand-written CreateBody's Row() can set anything on
// the struct it builds, and the capability would then be advisory.
//
// It runs before the insert and therefore before BeforeCreate, which is the
// whole point: a hook may still supply the value. That ordering is what makes
// `ReadOnly` mean "the database or a hook owns this" rather than "nothing can
// ever put a value here".
func (b *binding[T]) clearReadOnly(value *T) {
	if len(b.readOnly) == 0 {
		return
	}
	rv := reflect.ValueOf(value).Elem()
	for _, index := range b.readOnly {
		field, ok := fieldAt(rv, index)
		if !ok {
			continue
		}
		field.SetZero()
	}
}

// fieldAt walks a reflect index path, which may traverse embedded structs.
//
// It stops at a nil embedded pointer rather than allocating one: there is no
// value behind it to clear, and allocating would add a struct the caller never
// asked for.
func fieldAt(v reflect.Value, index []int) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v, true
}

func bind[T any](opts Options) (*binding[T], error) {
	m := sqlb.ModelOf[T]()
	b := &binding[T]{
		opts:       opts,
		model:      m,
		jsonName:   make(map[string]string, len(m.Columns)),
		selectable: selectableFor(m, opts.Computed),
	}

	rt := reflect.TypeFor[T]()
	for _, col := range m.Columns {
		name, err := jsonNameOf(rt, col)
		if err != nil {
			return nil, err
		}
		b.jsonName[col.Name] = name
		if col.ReadOnly {
			b.readOnly = append(b.readOnly, col.Index)
			continue
		}
		if !col.Hidden {
			b.writable = append(b.writable, col)
		}
	}

	// A hidden column carrying a JSON name would be serialised by any code
	// that marshalled the struct directly — a debug log, a hand-written
	// handler — so the mismatch is worth reporting where it is introduced.
	for _, col := range m.Columns {
		if col.Hidden && b.jsonName[col.Name] != "" {
			return nil, fmt.Errorf(
				"rest: %s.%s is hidden but has json tag %q; hidden columns need `json:\"-\"` so they cannot be marshalled by accident",
				m.Type, col.Field, b.jsonName[col.Name])
		}
	}
	// Computed names a column the model computes. An unknown one, or a stored
	// one, is a mounting error for the reason Expandable's is: at request time
	// it would parse cleanly and quietly serve a resource missing the value
	// somebody declared it should carry.
	for _, name := range opts.Computed {
		col := m.Column(name)
		switch {
		case col == nil:
			return nil, fmt.Errorf(
				"rest: %s declares Computed %q, but %s has no such column (have: %s)",
				opts.name(), name, m.Type, strings.Join(m.ColumnNames(), ", "))
		case !col.Computed():
			return nil, fmt.Errorf(
				"rest: %s declares Computed %q, but %s stores that column rather than computing it; "+
					"a stored column is already in the response and does not need declaring",
				opts.name(), name, m.Type)
		}
	}

	// Expandable names a relation, not a column, and an unknown one has to fail
	// here: at request time the parameter parses cleanly against Options and the
	// response would come back 200 with the relation missing.
	for _, name := range opts.Expandable {
		if m.Relation(name) == nil {
			return nil, fmt.Errorf(
				"rest: %s declares Expandable %q, but %s has no such relation (declared: %s); "+
					"a relation is a field tagged `sqlb:\"expands=<column>\"` beside a column tagged `expand`",
				opts.Path, name, m.Type.Name(), strings.Join(m.RelationNames(), ", "))
		}
	}

	return b, nil
}

// jsonNameOf resolves the JSON property a column serialises as, following the
// same rules encoding/json does.
func jsonNameOf(rt reflect.Type, col *sqlb.ColumnInfo) (string, error) {
	sf, err := fieldByIndex(rt, col.Index)
	if err != nil {
		return "", err
	}
	tag, ok := sf.Tag.Lookup("json")
	if !ok {
		return sf.Name, nil
	}
	name, _, _ := strings.Cut(tag, ",")
	switch name {
	case "-":
		return "", nil
	case "":
		return sf.Name, nil
	}
	return name, nil
}

// fieldByIndex walks an index path over a type, tolerating embedded pointers.
// reflect.Type.FieldByIndex panics on a path it cannot walk; this reports.
func fieldByIndex(t reflect.Type, index []int) (reflect.StructField, error) {
	var sf reflect.StructField
	for i, x := range index {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || x >= t.NumField() {
			return sf, fmt.Errorf("rest: cannot resolve field at index %v of %s", index[:i+1], t)
		}
		sf = t.Field(x)
		t = sf.Type
	}
	return sf, nil
}

// columnsFor resolves a projection — the ?select list, or every selectable
// column when the request named none — to the columns to serialise.
func (b *binding[T]) columnsFor(selected []string) []*sqlb.ColumnInfo {
	if len(selected) == 0 {
		return b.selectable
	}
	out := make([]*sqlb.ColumnInfo, 0, len(selected))
	for _, name := range selected {
		if col := b.model.Column(name); col != nil && !col.Hidden {
			out = append(out, col)
		}
	}
	return out
}

// row is one serialised model row, restricted to a projection.
//
// It exists because a projected scan leaves the unselected fields at their zero
// value, and marshalling the struct would report `"title": ""` for a column the
// query never read — indistinguishable from a genuinely empty title. Adding
// omitempty everywhere would hide real empty values instead, which is the same
// lie in the other direction. So the projection decides which keys exist.
type row[T any] struct {
	value T
	cols  []*sqlb.ColumnInfo
	names map[string]string
	// expand are the relations this request asked to resolve. They are not
	// columns, so they are serialised after them, under the relation's own
	// name — which is the JSON name of the field the expansion landed in.
	expand []*sqlb.RelationInfo
}

// MarshalJSON writes only the projected columns, in projection order, followed
// by whatever relations the request expanded.
func (r row[T]) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	rv := reflect.ValueOf(r.value)

	// A separator counter rather than the loop index: a column can be skipped
	// (no JSON name), and keying the comma off the index would emit a leading
	// one the moment the first column is the skipped one.
	written := 0
	write := func(name string, v any) error {
		if written > 0 {
			buf.WriteByte(',')
		}
		written++
		key, err := json.Marshal(name)
		if err != nil {
			return err
		}
		buf.Write(key)
		buf.WriteByte(':')
		encoded, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(encoded)
		return nil
	}

	for _, col := range r.cols {
		name := r.names[col.Name]
		if name == "" {
			continue
		}
		fv, ok := valueByIndex(rv, col.Index)
		if !ok {
			if written > 0 {
				buf.WriteByte(',')
			}
			written++
			key, err := json.Marshal(name)
			if err != nil {
				return nil, err
			}
			buf.Write(key)
			buf.WriteString(":null")
			continue
		}
		if err := write(name, fv.Interface()); err != nil {
			return nil, fmt.Errorf("rest: encoding %s: %w", col.Name, err)
		}
	}

	for _, rel := range r.expand {
		fv, ok := valueByIndex(rv, rel.Index)
		if !ok {
			continue
		}
		if err := write(rel.Name, fv.Interface()); err != nil {
			return nil, fmt.Errorf("rest: encoding expanded %s: %w", rel.Name, err)
		}
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Schema reports the row's OpenAPI schema as T's, so the document describes the
// model rather than this wrapper. Every property is optional, because ?select
// may leave any of them out.
func (r row[T]) Schema(reg huma.Registry) *huma.Schema {
	s := reg.Schema(reflect.TypeFor[T](), true, "")
	if resolved := deref(reg, s); resolved != nil {
		resolved.Required = nil
	}
	return s
}

// deref follows a $ref back to the schema it names, so that a registered
// component can be amended.
func deref(reg huma.Registry, s *huma.Schema) *huma.Schema {
	if s == nil {
		return nil
	}
	if s.Ref == "" {
		return s
	}
	return reg.SchemaFromRef(s.Ref)
}

// valueByIndex walks an index path over a value, reporting rather than
// panicking when it meets a nil embedded pointer.
func valueByIndex(v reflect.Value, index []int) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 {
			for v.Kind() == reflect.Pointer {
				if v.IsNil() {
					return reflect.Value{}, false
				}
				v = v.Elem()
			}
		}
		if v.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		v = v.Field(x)
	}
	return v, true
}

// rowsOf wraps scanned rows for serialisation under a projection.
func (b *binding[T]) rowsOf(values []T, cols []*sqlb.ColumnInfo, expand []string) []row[T] {
	// Resolved once for the page rather than per row: the relations are the
	// same for every row in it, and the lookup is a scan of a short slice.
	rels := b.relationsFor(expand)
	out := make([]row[T], len(values))
	for i, v := range values {
		out[i] = row[T]{value: v, cols: cols, names: b.jsonName, expand: rels}
	}
	return out
}

// relationsFor resolves the request's expand names to the relations to
// serialise. Names are already validated — by the parser against
// Options.Expandable, and by bind against the model — so an unknown one here
// would be a bug rather than bad input, and is skipped rather than guessed at.
func (b *binding[T]) relationsFor(names []string) []*sqlb.RelationInfo {
	if len(names) == 0 {
		return nil
	}
	out := make([]*sqlb.RelationInfo, 0, len(names))
	for _, name := range names {
		if rel := b.model.Relation(name); rel != nil {
			out = append(out, rel)
		}
	}
	return out
}

// selectableFor is the resource's projection: every non-hidden column, minus
// the computed ones it did not ask for.
//
// Model.Selectable cannot answer this on its own — it is model-wide, and the
// same model may be mounted twice with different computed sets, which is the
// case that made a shared model expensive to read (#92).
func selectableFor(m *sqlb.Model, computed []string) []*sqlb.ColumnInfo {
	wanted := make(map[string]bool, len(computed))
	for _, name := range computed {
		wanted[name] = true
	}
	out := make([]*sqlb.ColumnInfo, 0, len(m.Columns))
	for _, col := range m.Selectable() {
		if col.Computed() && !wanted[col.Name] {
			continue
		}
		out = append(out, col)
	}
	return out
}
