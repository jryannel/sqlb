package studio

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// writableColumns is every column a create or update body may carry. Neither
// operation is asked to distinguish further — the manifest doesn't say a
// column is create-only or update-only, so this form doesn't guess one.
func writableColumns(t *schema.TableManifest) []schema.ColumnManifest {
	var out []schema.ColumnManifest
	for _, c := range t.Columns {
		if c.ReadOnly || c.Computed || c.Name == t.PrimaryKey {
			continue
		}
		out = append(out, c)
	}
	return out
}

// formField is one input on the create/edit form. The widget choice is
// deliberately narrow: a checkbox for bool and a select for a declared enum
// are the only two cases the manifest answers unambiguously (bool has
// exactly two legal values; Enum lists the exact set). Everything else is
// plain text, with the declared type shown as a hint — the same "carries
// nothing an author can decide" line ADR-0053 draws for the rest of this
// tool, applied to a form widget instead of a row label.
type formField struct {
	Name     string
	Kind     string // "checkbox" | "select" | "text"
	Value    string
	Checked  bool
	Options  []string
	Nullable bool
	Hint     string
}

func hintFor(c schema.ColumnManifest) string {
	h := c.Type
	if c.Array {
		h += "[], comma-separated"
	}
	if c.Nullable {
		h += ", optional"
	}
	return h
}

// editValue is dispValue with one exception: an array is joined with commas
// rather than rendered as a JSON array, because that's the format
// encodeFieldValue's submit path expects back — the two have to agree on one
// spelling or a value round-trips through an edit form corrupted.
func editValue(c schema.ColumnManifest, val any) string {
	if val == nil {
		return ""
	}
	if !c.Array {
		return dispValue(val)
	}
	arr, ok := val.([]any)
	if !ok {
		return dispValue(val)
	}
	parts := make([]string, 0, len(arr))
	for _, e := range arr {
		parts = append(parts, dispValue(e))
	}
	return strings.Join(parts, ", ")
}

// buildFormFields renders writableColumns against row's current values, or
// empty values when row is nil (the create form).
func buildFormFields(t *schema.TableManifest, row map[string]any) []formField {
	var fields []formField
	for _, c := range writableColumns(t) {
		var val any
		if row != nil {
			val = row[wireOf(c)]
		}
		f := formField{Name: c.Name, Nullable: c.Nullable, Hint: hintFor(c)}
		switch {
		case c.Type == "bool" && !c.Array:
			f.Kind = "checkbox"
			if b, ok := val.(bool); ok {
				f.Checked = b
			}
		case len(c.Enum) > 0:
			f.Kind = "select"
			f.Options = c.Enum
			if val != nil {
				f.Value = dispValue(val)
			}
		default:
			f.Kind = "text"
			f.Value = editValue(c, val)
		}
		fields = append(fields, f)
	}
	return fields
}

// formFieldsFromForm rebuilds the field list from a rejected submission, so
// an error redisplay shows what the operator typed rather than reverting to
// the row's last-fetched values.
func formFieldsFromForm(t *schema.TableManifest, form url.Values) []formField {
	var fields []formField
	for _, c := range writableColumns(t) {
		f := formField{Name: c.Name, Nullable: c.Nullable, Hint: hintFor(c)}
		switch {
		case c.Type == "bool" && !c.Array:
			f.Kind = "checkbox"
			f.Checked = form.Has(c.Name)
		case len(c.Enum) > 0:
			f.Kind = "select"
			f.Options = c.Enum
			f.Value = form.Get(c.Name)
		default:
			f.Kind = "text"
			f.Value = form.Get(c.Name)
		}
		fields = append(fields, f)
	}
	return fields
}

// scalarValue converts one submitted text value into the JSON type the
// column's declared type implies. This is encoding, not a widget guess: the
// manifest already states the type, so "5" becomes the number 5 rather than
// the string "5" mechanically, the same way json.Marshal would for a typed
// field this tool has none of.
func scalarValue(colType, raw string) (any, error) {
	switch colType {
	case "smallint", "int", "bigint", "real", "float", "numeric":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", raw)
		}
		return f, nil
	default:
		return raw, nil
	}
}

func encodeFieldValue(c schema.ColumnManifest, raw string) (any, error) {
	if !c.Array {
		return scalarValue(c.Type, raw)
	}
	parts := strings.Split(raw, ",")
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := scalarValue(c.Type, p)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// actionHint mirrors hintFor for an action body property. ActionProperty has
// no Array field (ADR-0043's body vocabulary doesn't carry one), so there is
// no array case to add here.
func actionHint(p schema.ActionProperty) string {
	h := p.Type
	if p.Nullable {
		h += ", optional"
	}
	return h
}

func buildActionFields(body []schema.ActionProperty) []formField {
	var fields []formField
	for _, p := range body {
		f := formField{Name: p.Name, Nullable: p.Nullable, Hint: actionHint(p)}
		switch {
		case p.Type == "bool":
			f.Kind = "checkbox"
		case len(p.Enum) > 0:
			f.Kind = "select"
			f.Options = p.Enum
		default:
			f.Kind = "text"
		}
		fields = append(fields, f)
	}
	return fields
}

func actionFieldsFromForm(body []schema.ActionProperty, form url.Values) []formField {
	var fields []formField
	for _, p := range body {
		f := formField{Name: p.Name, Nullable: p.Nullable, Hint: actionHint(p)}
		switch {
		case p.Type == "bool":
			f.Kind = "checkbox"
			f.Checked = form.Has(p.Name)
		case len(p.Enum) > 0:
			f.Kind = "select"
			f.Options = p.Enum
			f.Value = form.Get(p.Name)
		default:
			f.Kind = "text"
			f.Value = form.Get(p.Name)
		}
		fields = append(fields, f)
	}
	return fields
}

// parseActionBody encodes a submitted form into the JSON body a declared
// action expects, keyed by the property's wire spelling. ActionProperty
// carries no precomputed Wire the way ColumnManifest does, so this derives
// it the same way a client with only this manifest would have to: the
// declared WireCase applied to the property's name (schema/wire.go).
func parseActionBody(wire schema.WireCase, body []schema.ActionProperty, form url.Values) (map[string]any, error) {
	out := map[string]any{}
	for _, p := range body {
		key := wire.WireName(p.Name)
		if p.Type == "bool" {
			out[key] = form.Has(p.Name)
			continue
		}
		if p.Nullable && form.Has(p.Name+"__clear") {
			out[key] = nil
			continue
		}
		raw := form.Get(p.Name)
		if raw == "" {
			continue
		}
		val, err := scalarValue(p.Type, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.Name, err)
		}
		out[key] = val
	}
	return out, nil
}

// parseFormBody turns a submitted form into a JSON body keyed by wire name.
// A bool is always present (a checkbox has no "unchanged" state). A blank
// text/select value is omitted — "no change" on edit, "use the column's own
// default" on create — unless its "<name>__clear" companion is present, which
// forces an explicit null so a nullable field can be cleared rather than only
// ever grown.
func parseFormBody(t *schema.TableManifest, form url.Values) (map[string]any, error) {
	body := map[string]any{}
	for _, c := range writableColumns(t) {
		wire := wireOf(c)
		if c.Type == "bool" && !c.Array {
			body[wire] = form.Has(c.Name)
			continue
		}
		if c.Nullable && form.Has(c.Name+"__clear") {
			body[wire] = nil
			continue
		}
		raw := form.Get(c.Name)
		if raw == "" {
			continue
		}
		val, err := encodeFieldValue(c, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Name, err)
		}
		body[wire] = val
	}
	return body, nil
}
