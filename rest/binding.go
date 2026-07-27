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

	// selectable is the default projection: every non-hidden column.
	selectable []*sqlb.ColumnInfo

	// writable is what a request body may set. Read-only columns are excluded
	// because the database or a hook owns them, and hidden ones because a
	// column that never leaves the process should not be settable from
	// outside it either.
	writable []*sqlb.ColumnInfo

	// readOnly is the complement, as names, for Insert.Omit.
	readOnly []string
}

func bind[T any](opts Options) (*binding[T], error) {
	m := sqlb.ModelOf[T]()
	b := &binding[T]{
		opts:       opts,
		model:      m,
		jsonName:   make(map[string]string, len(m.Columns)),
		selectable: m.Selectable(),
	}

	rt := reflect.TypeFor[T]()
	for _, col := range m.Columns {
		name, err := jsonNameOf(rt, col)
		if err != nil {
			return nil, err
		}
		b.jsonName[col.Name] = name
		if col.ReadOnly {
			b.readOnly = append(b.readOnly, col.Name)
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
}

// MarshalJSON writes only the projected columns, in projection order.
func (r row[T]) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	rv := reflect.ValueOf(r.value)
	for i, col := range r.cols {
		name := r.names[col.Name]
		if name == "" {
			continue
		}
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')

		fv, ok := valueByIndex(rv, col.Index)
		if !ok {
			buf.WriteString("null")
			continue
		}
		encoded, err := json.Marshal(fv.Interface())
		if err != nil {
			return nil, fmt.Errorf("rest: encoding %s: %w", col.Name, err)
		}
		buf.Write(encoded)
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
func (b *binding[T]) rowsOf(values []T, cols []*sqlb.ColumnInfo) []row[T] {
	out := make([]row[T], len(values))
	for i, v := range values {
		out[i] = row[T]{value: v, cols: cols, names: b.jsonName}
	}
	return out
}
