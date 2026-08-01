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

	// SortNulls is where NULLs sit whenever this column is sorted on, in either
	// direction. The zero value leaves Postgres's own default, which follows
	// the direction rather than being one placement. It is declared on the
	// column because it is a property of what the column means (#88), so a
	// request does not — and cannot — ask for it.
	SortNulls NullsOrder

	// Obligations, from the same tag. Nothing on the request path reads
	// either: they are the schema's statement that this model's rows are
	// confined by something, and they are checked once, where a resource is
	// mounted.
	Scoped     bool
	SoftDelete bool

	// Expr is the SQL a computed column renders as, in place of its name. It
	// arrives from the model's ComputedColumns method or from Describe, never
	// from a struct tag: the expression is SQL, and a tag is a comma-separated
	// list (ADR-0041).
	Expr string
	// Needs names the binds Expr's `?` placeholders take, in order. A computed
	// column with none is row-local; one with a bind is answered per request,
	// and the value comes from Builder.Bind — which a BeforeQuery hook calls,
	// and which rest refuses to mount a resource without.
	Needs []string
}

// Computed reports whether this column is an expression rather than storage.
// Such a column is projected and may be filtered or sorted on, and it is never
// written: no insert names it, no update sets it, and no migration creates it.
func (c *ColumnInfo) Computed() bool { return c.Expr != "" }

// Computed declares one derived column: a SQL expression the compiler renders
// wherever the column is named, rather than a value the table stores.
//
// Generated models return these from ComputedColumns; a hand-written one can
// implement the method itself, or say the same thing through Describe.
type Computed struct {
	// Name is the column name — the key in the JSON, the name a filter or a
	// sort spells, and the alias the projection scans back through.
	Name string
	// Expr is the SQL, written against this table's own columns. Each `?` in
	// it takes the bind named at the matching position of Needs; a doubled
	// `??` is a literal question mark, as in Raw.
	Expr string
	// Needs names the binds Expr takes, in order.
	Needs []string
}

// Deriver is a model that declares computed columns. Generated models with a
// schema.Computed field implement it.
type Deriver interface {
	ComputedColumns() []Computed
}

// Model is the reflected mapping between a Go struct and a table.
type Model struct {
	Type    reflect.Type
	Table   string
	Columns []*ColumnInfo
	PK      *ColumnInfo

	// Derived are the computed columns, in declaration order. They are also in
	// Columns — a computed column is a column, which is what makes Hidden,
	// Filterable and the whole capability vocabulary apply to it unchanged —
	// and this is the list for the callers that need only them, the mount check
	// among them.
	Derived []*ColumnInfo

	// Relations are the expandable references this model declares — the
	// struct fields carrying an expanded row rather than a column of their
	// own. They are not columns: a relation field is `db:"-"`, so nothing
	// selects, inserts or updates it.
	Relations []*RelationInfo

	// Scope and Soft are the columns that declared an obligation, or nil. They
	// are resolved here so that the check at mount time is a field read rather
	// than a scan, and so that the error can name the column that asked.
	Scope *ColumnInfo
	Soft  *ColumnInfo

	byName map[string]*ColumnInfo
	// byDerived is Derived keyed by name, built once here so that compiling a
	// statement is a map lookup per column reference rather than a scan.
	byDerived map[string]*ColumnInfo
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
	if err := applyComputed(m); err != nil {
		return nil, err
	}
	return m, nil
}

// applyComputed attaches the expressions a model declares to the columns they
// belong to.
//
// A computed column is an ordinary struct field with an ordinary `db` tag —
// that is what makes it scan, and what makes every capability apply to it — so
// the expression is the only thing that cannot be said in a tag, and this is
// where it is said.
func applyComputed(m *Model) error {
	d, ok := reflect.New(m.Type).Interface().(Deriver)
	if !ok {
		return nil
	}
	for _, comp := range d.ComputedColumns() {
		col := m.byName[comp.Name]
		if col == nil {
			return fmt.Errorf(
				"sqlb: model %s computes %q but maps no field to that column (columns: %s)",
				m.Type, comp.Name, strings.Join(m.ColumnNames(), ", "))
		}
		if err := setComputed(m, col, comp.Expr, comp.Needs); err != nil {
			return err
		}
	}
	return nil
}

// setComputed marks one column computed, refusing the combinations that would
// be silently wrong.
func setComputed(m *Model, col *ColumnInfo, expr string, needs []string) error {
	if strings.TrimSpace(expr) == "" {
		return fmt.Errorf("sqlb: model %s computes %q with an empty expression", m.Type, col.Name)
	}
	if n := placeholderCount(expr); n != len(needs) {
		return fmt.Errorf(
			"sqlb: model %s computes %q with %d placeholder(s) but names %d bind(s) (%s); "+
				"each `?` takes the bind at the matching position, and `??` is a literal question mark",
			m.Type, col.Name, n, len(needs), strings.Join(needs, ", "))
	}
	if col.Searchable {
		// ?search fans out over text columns with ILIKE (ADR-0037). There is no
		// reading of that over an expression which is not either a lie about
		// what was searched or a table scan nobody asked for.
		return fmt.Errorf("sqlb: model %s computes %q and marks it searchable; a computed column cannot be part of the ?search fan-out",
			m.Type, col.Name)
	}
	// Nothing writes an expression. Marking it here rather than asking every
	// caller to check Computed() is what keeps the create and update bodies,
	// the insert column list and the REST write paths correct without knowing
	// this feature exists.
	col.ReadOnly = true
	col.Expr = expr
	col.Needs = append([]string(nil), needs...)
	if m.byDerived == nil {
		m.byDerived = map[string]*ColumnInfo{}
	}
	if _, again := m.byDerived[col.Name]; !again {
		m.Derived = append(m.Derived, col)
	}
	m.byDerived[col.Name] = col
	return nil
}

// placeholderCount counts the binds an expression takes, treating `??` as an
// escaped literal exactly as Raw does.
func placeholderCount(expr string) int {
	n := 0
	for i := 0; i < len(expr); i++ {
		if expr[i] != '?' {
			continue
		}
		if i+1 < len(expr) && expr[i+1] == '?' {
			i++
			continue
		}
		n++
	}
	return n
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

		if col.Scoped {
			if m.Scope != nil {
				return fmt.Errorf("sqlb: model %s declares two scope columns (%s and %s)", m.Type, m.Scope.Field, sf.Name)
			}
			m.Scope = col
		}

		if col.SoftDelete {
			if m.Soft != nil {
				return fmt.Errorf("sqlb: model %s declares two soft-delete columns (%s and %s)", m.Type, m.Soft.Field, sf.Name)
			}
			m.Soft = col
		}

		m.Columns = append(m.Columns, col)
		m.byName[name] = col
	}
	return nil
}

func applyCapabilities(c *ColumnInfo, tag string) {
	for _, part := range strings.Split(tag, ",") {
		// A capability may carry an argument after a colon. Only `sort` does
		// today; splitting here rather than in that one case keeps an unknown
		// `name:arg` falling through to the same silent ignore an unknown bare
		// name already gets, instead of being read as a column capability
		// nobody declared.
		name, arg, _ := strings.Cut(strings.TrimSpace(part), ":")
		switch name {
		case "":
		case "pk":
			c.PrimaryKey = true
			c.ReadOnly = true
			c.Filterable = true
		case "filter":
			c.Filterable = true
		case "sort":
			c.Sortable = true
			switch arg {
			case "nullsfirst":
				c.SortNulls = NullsFirst
			case "nullslast":
				c.SortNulls = NullsLast
			}
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
		case "scope":
			c.Scoped = true
		case "softdelete":
			c.SoftDelete = true
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
