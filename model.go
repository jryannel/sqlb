package sqlb

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

// Tabler lets a model name its own table. Generated models implement it;
// hand-written ones can too. Without it the table name is derived from the type
// name, so `type User struct{}` maps to "users".
type Tabler interface {
	TableName() string
}

// ColumnInfo describes one mapped column of a model.
type ColumnInfo struct {
	// Name is the SQL column name.
	Name string
	// Field is the Go struct field name.
	Field string
	// Index is the reflect field index path, which may traverse embedded structs.
	Index []int
	// Type is the Go type of the struct field.
	Type reflect.Type
	// Nullable reports whether the Go field is a pointer, and so may hold NULL.
	Nullable bool
	// HasDefault reports that the column has a database default. Inserts omit
	// such a column when its Go value is the zero value, so the database fills
	// it rather than being handed a zero.
	HasDefault bool

	// Capabilities, read back from the `sqlb` struct tag that codegen writes
	// from the schema declaration.
	PrimaryKey bool
	Filterable bool
	Sortable   bool
	Searchable bool
	Expandable bool
	ReadOnly   bool
	Immutable  bool
	Hidden     bool
}

// Model is the reflected mapping between a Go struct and a table.
type Model struct {
	Type    reflect.Type
	Table   string
	Columns []*ColumnInfo
	PK      *ColumnInfo

	// Relations are the expandable references this model declares — the
	// struct fields carrying an expanded row rather than a column of their
	// own. They are not columns: a relation field is `db:"-"`, so nothing
	// selects, inserts or updates it.
	Relations []*RelationInfo

	byName map[string]*ColumnInfo
	// inUse is set the first time a statement is built against this model.
	// Describe refuses to mutate a model past that point: doing so is a data
	// race against every in-flight query, and a description that silently
	// half-applied would be worse than one that never ran.
	inUse atomic.Bool
}

// markInUse records that a statement has been built against this model, closing
// it to further description.
func (m *Model) markInUse() { m.inUse.Store(true) }

// InUse reports whether a statement has been built against this model.
func (m *Model) InUse() bool { return m.inUse.Load() }

// Column returns the named column, or nil.
func (m *Model) Column(name string) *ColumnInfo { return m.byName[name] }

// ColumnNames returns every mapped column name in declaration order.
func (m *Model) ColumnNames() []string {
	out := make([]string, len(m.Columns))
	for i, c := range m.Columns {
		out[i] = c.Name
	}
	return out
}

// Selectable returns the columns a REST response may contain: everything not
// marked hidden.
func (m *Model) Selectable() []*ColumnInfo {
	out := make([]*ColumnInfo, 0, len(m.Columns))
	for _, c := range m.Columns {
		if !c.Hidden {
			out = append(out, c)
		}
	}
	return out
}

var modelCache sync.Map // reflect.Type -> *Model

// ModelOf returns the model for T, reflecting over it once and caching the
// result. It panics if T is not a struct, which is a programming error rather
// than a runtime condition.
func ModelOf[T any]() *Model {
	var zero T
	t := reflect.TypeOf(&zero).Elem()
	if cached, found := modelCache.Load(t); found {
		m, ok := cached.(*Model)
		if !ok {
			panic(fmt.Sprintf("sqlb: model cache holds %T for %s", cached, t))
		}
		return m
	}
	m, err := buildModel(t)
	if err != nil {
		panic(err)
	}
	// A concurrent build for the same type is harmless: both are equivalent.
	actual, _ := modelCache.LoadOrStore(t, m)
	cached, ok := actual.(*Model)
	if !ok {
		panic(fmt.Sprintf("sqlb: model cache holds %T for %s", actual, t))
	}
	return cached
}

func buildModel(t reflect.Type) (*Model, error) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("sqlb: model type %s is %s, want struct", t, t.Kind())
	}

	m := &Model{Type: t, Table: tableNameFor(t), byName: map[string]*ColumnInfo{}}
	if err := collectColumns(m, t, nil); err != nil {
		return nil, err
	}
	if len(m.Columns) == 0 {
		return nil, fmt.Errorf("sqlb: model %s maps no columns (are its fields exported?)", t)
	}
	// After the columns, because a relation field may be declared above the
	// column it joins on.
	if err := resolveRelations(m); err != nil {
		return nil, err
	}
	return m, nil
}

// modelOfType is ModelOf without the type parameter, for resolving a relation's
// target from a reflect.Type. It shares the same cache, so a model reached
// through a relation and one reached through ModelOf are the same value.
func modelOfType(t reflect.Type) (*Model, error) {
	if cached, found := modelCache.Load(t); found {
		m, ok := cached.(*Model)
		if !ok {
			return nil, fmt.Errorf("sqlb: model cache holds %T for %s", cached, t)
		}
		return m, nil
	}
	m, err := buildModel(t)
	if err != nil {
		return nil, err
	}
	actual, _ := modelCache.LoadOrStore(t, m)
	cached, ok := actual.(*Model)
	if !ok {
		return nil, fmt.Errorf("sqlb: model cache holds %T for %s", actual, t)
	}
	return cached, nil
}

func collectColumns(m *Model, t reflect.Type, prefix []int) error {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag, hasTag := sf.Tag.Lookup("db")
		index := append(append([]int(nil), prefix...), i)

		// A relation field holds an expanded row rather than a column. It is
		// checked before the `db:"-"` skip below, because `db:"-"` is exactly
		// how it declares that it is not a column.
		rt, isRelation, err := expansionOf(sf.Tag.Get("sqlb"))
		if err != nil {
			return fmt.Errorf("%w (field %s.%s)", err, t.Name(), sf.Name)
		}
		if isRelation {
			if !sf.IsExported() {
				return fmt.Errorf("sqlb: field %s.%s expands %q but is not exported", t.Name(), sf.Name, rt.fk)
			}
			rel, err := newRelation(sf, index, rt)
			if err != nil {
				return err
			}
			m.Relations = append(m.Relations, rel)
			continue
		}

		if tag == "-" {
			continue
		}

		// An untagged embedded struct contributes its own fields, so shared
		// column sets can live in a mixin type.
		if sf.Anonymous && !hasTag {
			ft := sf.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && !isScannable(ft) {
				if err := collectColumns(m, ft, index); err != nil {
					return err
				}
				continue
			}
		}

		if !sf.IsExported() {
			continue
		}

		name := tag
		if name == "" {
			name = snake(sf.Name)
		}
		if prev, dup := m.byName[name]; dup {
			return fmt.Errorf("sqlb: model %s maps column %q twice (%s and %s)", m.Type, name, prev.Field, sf.Name)
		}

		col := &ColumnInfo{
			Name:     name,
			Field:    sf.Name,
			Index:    index,
			Type:     sf.Type,
			Nullable: sf.Type.Kind() == reflect.Pointer,
		}
		applyCapabilities(col, sf.Tag.Get("sqlb"))

		if col.PrimaryKey {
			if m.PK != nil {
				return fmt.Errorf("sqlb: model %s declares two primary keys (%s and %s)", m.Type, m.PK.Field, sf.Name)
			}
			m.PK = col
		}

		m.Columns = append(m.Columns, col)
		m.byName[name] = col
	}
	return nil
}

func applyCapabilities(c *ColumnInfo, tag string) {
	for _, part := range strings.Split(tag, ",") {
		switch strings.TrimSpace(part) {
		case "":
		case "pk":
			c.PrimaryKey = true
			c.ReadOnly = true
			c.Filterable = true
		case "filter":
			c.Filterable = true
		case "sort":
			c.Sortable = true
		case "search":
			c.Searchable = true
			c.Filterable = true
		case "expand":
			c.Expandable = true
		case "readonly":
			c.ReadOnly = true
		case "immutable":
			c.Immutable = true
		case "hidden":
			c.Hidden = true
		case "default":
			c.HasDefault = true
		}
	}
}

// isScannable reports whether a struct type should be scanned as a single
// value rather than decomposed into columns. time.Time and the sql.Null* types
// are structs but map to one column each.
func isScannable(t reflect.Type) bool {
	ptr := reflect.PointerTo(t)
	return ptr.Implements(scannerType) || t.Implements(valuerType) ||
		(t.PkgPath() == "time" && t.Name() == "Time")
}

func tableNameFor(t reflect.Type) string {
	// Check both value and pointer receivers for TableName.
	if v, ok := reflect.New(t).Interface().(Tabler); ok {
		return v.TableName()
	}
	return plural(snake(t.Name()))
}

// snake converts a Go identifier to snake_case, keeping runs of capitals
// together so that "UserID" becomes "user_id" rather than "user_i_d".
func snake(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && !unicode.IsUpper(runes[i-1])
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if i > 0 && (prevLower || nextLower) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// plural is a deliberately small English pluraliser. Anything it gets wrong is
// fixed by implementing Tabler on the model, which generated code always does.
func plural(s string) string {
	switch {
	case s == "":
		return s
	case strings.HasSuffix(s, "s"), strings.HasSuffix(s, "x"), strings.HasSuffix(s, "z"),
		strings.HasSuffix(s, "ch"), strings.HasSuffix(s, "sh"):
		return s + "es"
	case strings.HasSuffix(s, "y") && len(s) > 1 && !isVowel(s[len(s)-2]):
		return s[:len(s)-1] + "ies"
	default:
		return s + "s"
	}
}

func isVowel(b byte) bool { return strings.IndexByte("aeiou", b) >= 0 }
