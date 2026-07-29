package schema

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ManifestVersion is bumped when the manifest shape changes incompatibly.
const ManifestVersion = "1"

// Manifest is a machine-readable description of a schema: every table, every
// column, and — the part that matters most — exactly what a client may filter,
// sort, search and expand on each exposed resource.
//
// It reports capabilities that work, not capabilities that are declared. The
// two coincide today; where they ever diverge again, this file is what has to
// keep telling the truth.
//
// It exists because reading a Go DSL to answer "what can I query here?" is a
// poor interface for a program. The manifest answers it directly, in one file,
// with worked example requests. Emit it next to the generated code and point
// tooling at it.
type Manifest struct {
	Version   string          `json:"version"`
	Module    string          `json:"module,omitempty"`
	Tables    []TableManifest `json:"tables"`
	Operators []OperatorDoc   `json:"filterOperators"`
	Params    []ParamDoc      `json:"reservedParams"`
}

// TableManifest describes one table.
type TableManifest struct {
	Name       string           `json:"name"`
	Module     string           `json:"module,omitempty"`
	LocalName  string           `json:"localName,omitempty"`
	Comment    string           `json:"comment,omitempty"`
	PrimaryKey string           `json:"primaryKey,omitempty"`
	Columns    []ColumnManifest `json:"columns"`
	Indexes    []IndexManifest  `json:"indexes,omitempty"`

	// CollectedBy describes the reverse relations pointing at this table: the
	// rows of another table that this one collects, and the name it knows them
	// by. Declared on the referencing side, which is where the column and the
	// constraint already live, so reading this table alone would otherwise not
	// show that its endpoint has them. ADR-0022.
	CollectedBy []InverseManifest `json:"collectedBy,omitempty"`

	REST *RESTManifest `json:"rest,omitempty"`
}

// InverseManifest describes one reverse relation from the target's side.
type InverseManifest struct {
	Name   string `json:"name"`
	Table  string `json:"table"`
	Column string `json:"column"`
	// Order is the column an expansion sorts the collected rows by, with a
	// leading "-" for descending. Empty means the primary key.
	Order string `json:"order,omitempty"`
	// Limit is how many rows one expansion returns at most, with the default
	// already resolved: a client reading this is never left to guess the cap.
	// Past it the response reports has_more and the caller pages the collected
	// table's own endpoint by Column.
	Limit int `json:"limit,omitempty"`
	// Expandable reports whether ?expand on this table may ask for it. A
	// relation that is named but not expandable is still described here,
	// because the relationship exists whether or not this endpoint serves it.
	Expandable bool `json:"expandable"`
}

// ColumnManifest describes one column. Hidden columns are omitted entirely
// rather than listed as hidden: the manifest is publishable, and a name is
// itself information.
type ColumnManifest struct {
	Name string `json:"name"`
	// Type names the element type of an array column, with Array set beside
	// it — the same split the declaration uses, so a consumer reading the
	// manifest sees the enum values and the varchar length attached to the
	// thing that has them.
	Type         string       `json:"type"`
	Array        bool         `json:"array,omitempty"`
	GoType       string       `json:"goType"`
	Nullable     bool         `json:"nullable,omitempty"`
	Comment      string       `json:"comment,omitempty"`
	Enum         []string     `json:"enum,omitempty"`
	HasDefault   bool         `json:"hasDefault,omitempty"`
	ReadOnly     bool         `json:"readOnly,omitempty"`
	Immutable    bool         `json:"immutable,omitempty"`
	Capabilities []string     `json:"capabilities,omitempty"`
	References   *RefManifest `json:"references,omitempty"`

	// Obligations, kept out of Capabilities because a capability is something
	// a request may reach and these are things the server must have done. A
	// client generator has no use for either; a reader auditing the boundary
	// has.
	Scoped     bool `json:"scoped,omitempty"`
	SoftDelete bool `json:"softDelete,omitempty"`
}

// RefManifest describes a relationship. External references carry a target
// string and no enforced constraint, so a reader can see the relationship even
// though the database does not.
type RefManifest struct {
	Relation string `json:"relation"`
	Table    string `json:"table,omitempty"`
	Column   string `json:"column,omitempty"`
	OnDelete string `json:"onDelete,omitempty"`
	External bool   `json:"external,omitempty"`
	Target   string `json:"target,omitempty"`
	Enforced bool   `json:"enforced"`
}

// IndexManifest describes a secondary index.
type IndexManifest struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
	Method  string   `json:"method,omitempty"`
}

// RESTManifest is the queryable surface of an exposed table: the single most
// useful thing in the document.
type RESTManifest struct {
	Path            string   `json:"path"`
	Operations      []string `json:"operations"`
	DefaultPageSize int      `json:"defaultPageSize"`
	MaxPageSize     int      `json:"maxPageSize"`
	MaxFilters      int      `json:"maxFilters,omitempty"`

	Filterable []string `json:"filterable"`
	Sortable   []string `json:"sortable"`
	Searchable []string `json:"searchable"`

	// Expandable names the relations ?expand may pull in. Each is the relation
	// name, not the foreign key column: ?expand=list, not ?expand=list_id.
	//
	// Only internal references appear. An ExternalRef crosses a module
	// boundary, which is exactly the join this schema will not perform.
	Expandable []string `json:"expandable,omitempty"`

	Examples []string `json:"examples,omitempty"`
}

// OperatorDoc documents one filter operator.
type OperatorDoc struct {
	Name    string `json:"name"`
	Form    string `json:"form"`
	Applies string `json:"applies"`
}

// ParamDoc documents one reserved query parameter.
type ParamDoc struct {
	Name string `json:"name"`
	Form string `json:"form"`
}

// BuildManifest describes every table in the registry.
func (r *Registry) BuildManifest() *Manifest {
	m := &Manifest{
		Version:   ManifestVersion,
		Module:    r.module,
		Operators: operatorDocs(),
		Params:    paramDocs(),
	}
	for _, t := range r.Tables() {
		m.Tables = append(m.Tables, t.manifest(r.Inverses(t)))
	}
	return m
}

// BuildManifest describes the default registry.
func BuildManifest() *Manifest { return defaultRegistry.BuildManifest() }

func (t *TableDef) manifest(inverses []InverseRelation) TableManifest {
	tm := TableManifest{Name: t.name, Comment: t.comment}
	if t.module != "" {
		tm.Module, tm.LocalName = t.module, t.local
	}
	if pk := t.PrimaryKey(); pk != nil {
		tm.PrimaryKey = pk.Desc().Name
	}

	for _, f := range t.fields {
		d := f.Desc()
		if d.Hidden {
			continue
		}
		cm := ColumnManifest{
			Name:       d.Name,
			Type:       string(d.Type),
			Array:      d.Array,
			GoType:     d.GoType(),
			Nullable:   d.Nullable,
			Comment:    d.Comment,
			Enum:       d.EnumValues,
			HasDefault: d.Default != nil,
			ReadOnly:   d.ReadOnly,
			Immutable:  d.Immutable,
			Scoped:     d.Scoped,
			SoftDelete: d.SoftDelete,
		}
		for _, c := range []struct {
			on   bool
			name string
		}{
			{d.Filterable, "filter"}, {d.Sortable, "sort"},
			{d.Searchable, "search"}, {d.Expandable, "expand"},
		} {
			if c.on {
				cm.Capabilities = append(cm.Capabilities, c.name)
			}
		}
		switch {
		case d.Ref != nil && d.Ref.External:
			cm.References = &RefManifest{
				Relation: d.Ref.Name,
				External: true,
				Target:   d.Ref.Target,
				Enforced: false,
			}
		case d.Ref != nil && d.Ref.Table != nil:
			cm.References = &RefManifest{
				Relation: d.Ref.Name,
				Table:    d.Ref.Table.name,
				Column:   d.Ref.Column,
				OnDelete: string(d.Ref.OnDelete),
				Enforced: true,
			}
		}
		tm.Columns = append(tm.Columns, cm)
	}

	for _, idx := range t.indexes {
		tm.Indexes = append(tm.Indexes, IndexManifest{
			Name: idx.Name, Columns: idx.Columns, Unique: idx.Unique, Method: idx.Method,
		})
	}

	for _, inv := range inverses {
		tm.CollectedBy = append(tm.CollectedBy, InverseManifest{
			Name:       inv.Name,
			Table:      inv.Table.Name(),
			Column:     inv.Column,
			Order:      inv.Order,
			Limit:      inv.Cap(),
			Expandable: inv.Expandable,
		})
	}

	if t.rest != nil {
		tm.REST = t.restManifest(inverses)
	}
	return tm
}

func (t *TableDef) restManifest(inverses []InverseRelation) *RESTManifest {
	rm := &RESTManifest{
		Path:            t.rest.Path,
		Operations:      strings.Split(t.rest.Ops.String(), "|"),
		DefaultPageSize: t.rest.DefaultPageSize,
		MaxPageSize:     t.rest.MaxPageSize,
		MaxFilters:      t.rest.MaxFilters,
		Filterable:      []string{},
		Sortable:        []string{},
		Searchable:      []string{},
	}
	for _, f := range t.fields {
		d := f.Desc()
		if d.Hidden {
			continue
		}
		if d.Filterable {
			rm.Filterable = append(rm.Filterable, d.Name)
		}
		if d.Sortable {
			rm.Sortable = append(rm.Sortable, d.Name)
		}
		if d.Searchable {
			rm.Searchable = append(rm.Searchable, d.Name)
		}
		// The relation name, not the column: ?expand names the relation, and a
		// caller reading this should not have to strip an "_id" to guess it.
		if d.Expandable && d.Ref != nil && !d.Ref.External {
			rm.Expandable = append(rm.Expandable, d.Ref.Name)
		}
	}
	// The reverse direction is expandable on this endpoint too, and a client
	// reading the vocabulary should not have to infer it from another table.
	for _, inv := range inverses {
		if inv.Expandable {
			rm.Expandable = append(rm.Expandable, inv.Name)
		}
	}
	rm.Examples = t.examples(rm)
	return rm
}

// examples renders a few requests that are valid against this resource. A
// worked example is worth more than a grammar to a caller assembling its first
// request, and unlike prose it can be checked against the parser.
func (t *TableDef) examples(rm *RESTManifest) []string {
	var out []string
	if len(rm.Filterable) > 0 {
		out = append(out, fmt.Sprintf("GET %s?%s=eq.VALUE", rm.Path, rm.Filterable[0]))
	}
	if len(rm.Sortable) > 0 {
		out = append(out, fmt.Sprintf("GET %s?sort=-%s&page=2&per_page=20", rm.Path, rm.Sortable[0]))
	}
	if len(rm.Searchable) > 0 {
		out = append(out, fmt.Sprintf("GET %s?search=TERM", rm.Path))
	}
	if len(rm.Expandable) > 0 {
		out = append(out, fmt.Sprintf("GET %s?expand=%s", rm.Path, rm.Expandable[0]))
	}
	if len(rm.Filterable) > 1 {
		out = append(out, fmt.Sprintf("GET %s?or=(%s.eq.A,%s.eq.B)",
			rm.Path, rm.Filterable[0], rm.Filterable[1]))
	}
	return out
}

func operatorDocs() []OperatorDoc {
	return []OperatorDoc{
		{"eq", "col=eq.VALUE (or col=VALUE)", "any filterable column"},
		{"ne", "col=ne.VALUE", "any filterable column"},
		{"gt", "col=gt.VALUE", "ordered columns"},
		{"gte", "col=gte.VALUE", "ordered columns"},
		{"lt", "col=lt.VALUE", "ordered columns"},
		{"lte", "col=lte.VALUE", "ordered columns"},
		{"in", "col=in.A,B,C", "any filterable column"},
		{"nin", "col=nin.A,B", "any filterable column"},
		{"between", "col=between.LO,HI", "ordered columns"},
		{"isnull", "col=isnull", "nullable columns"},
		{"notnull", "col=notnull", "nullable columns"},
		{"contains", "col=contains.TEXT", "text columns; wildcards escaped"},
		{"startswith", "col=startswith.TEXT", "text columns; wildcards escaped"},
		{"endswith", "col=endswith.TEXT", "text columns; wildcards escaped"},
		{"like", "col=like.PATTERN", "text columns; wildcards NOT escaped"},
		{"ilike", "col=ilike.PATTERN", "text columns; wildcards NOT escaped"},
	}
}

func paramDocs() []ParamDoc {
	return []ParamDoc{
		{"select", "select=id,name — projection; the primary key is always included"},
		{"sort", "sort=-created_at,name — leading '-' is descending"},
		{"search", "search=TERM — fans out over searchable columns"},
		// "expand" is not listed: paramDocs is unconditional, so listing it
		// would advertise expansion on every resource in the document,
		// including the ones that declare no expandable relation at all.
		{"page", "page=2 — 1-based"},
		{"per_page", "per_page=50 — capped by maxPageSize"},
		{"limit", "limit=50 — alternative to per_page"},
		{"offset", "offset=100 — alternative to page"},
		{"or", "or=(a.eq.1,b.gt.2) — explicit disjunction, nestable"},
		{"and", "and=(a.eq.1,b.gt.2) — explicit conjunction"},
	}
}

// JSON renders the manifest.
func (m *Manifest) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// WriteManifest writes the default registry's manifest to path, creating
// parent directories as needed.
func WriteManifest(path string) error {
	b, err := BuildManifest().JSON()
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, b, 0o644)
}
