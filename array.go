package sqlb

import (
	"database/sql"
	"database/sql/driver"
	"encoding"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Postgres array support.
//
// database/sql has no array case in either direction: the driver hands back
// `{a,b,"c d"}` as bytes, and driver.Value has no slice beyond []byte. The
// usual answer is pq.Array, and it is not available here — the engine is
// stdlib-only and a CI gate enforces it (ADR-0013), which is the property that
// makes importing sqlb cost a consumer nothing. So the array literal codec is
// written here, in both directions, and it lands at the two places every value
// already passes through: compiler.bind on the way out, and the scan target
// assignment in exec.go on the way back.
//
// The Go type stays the plain slice — []string, not a named wrapper — so a
// model described over an existing sqlc struct carries one unchanged. That is
// ADR-0033's decision and the reason for it.

// arrayElemKind reports whether t is a slice sqlb should encode as a Postgres
// array, and returns its element type.
//
// []byte is excluded: it is bytea, which the driver already handles, and
// encoding it as an array of smallints would corrupt every binary column. So is
// any slice whose element type carries its own driver.Valuer, since that type
// asked to be encoded its own way.
func arrayElemKind(t reflect.Type) (reflect.Type, bool) {
	if t == nil || t.Kind() != reflect.Slice {
		return nil, false
	}
	elem := t.Elem()
	if elem.Kind() == reflect.Uint8 {
		return nil, false
	}
	if t.Implements(valuerType) || reflect.PointerTo(t).Implements(scannerType) {
		return nil, false
	}
	if !encodableElem(elem) {
		return nil, false
	}
	return elem, true
}

// encodableElem reports whether one element has a Postgres array literal
// spelling this package knows how to write.
func encodableElem(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		// A NULL element is refused rather than encoded: `{a,NULL,b}` and NULL
		// are two different absences, and neither generated client can tell a
		// UI which one it is looking at (ADR-0033).
		return false
	}
	if t == timeType || t.Implements(textMarshalerType) {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// EncodeArray renders a Go slice as a Postgres array literal.
//
// It is exported because the escape hatches need it: a Raw fragment binding an
// array operand, or a SetExpr writing one, has no other way to produce a value
// the driver will accept.
//
// It accepts []any as well as a typed slice, because that is the shape the
// filter parser produces: `?tags=hasall.a,b` arrives as strings and leaves
// Coerce as the element's Go type, one value at a time.
func EncodeArray(v any) (string, error) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return "", fmt.Errorf("sqlb: cannot encode a nil value as a Postgres array")
	}
	t := rv.Type()
	if _, isArray := arrayElemKind(t); !isArray && !isAnySlice(t) {
		return "", fmt.Errorf("sqlb: %s is not a slice sqlb encodes as a Postgres array", t)
	}
	return encodeArrayValue(rv)
}

// isAnySlice reports whether t is a slice of interface values, whose elements
// are encoded by their dynamic type.
func isAnySlice(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Interface
}

func encodeArrayValue(rv reflect.Value) (string, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < rv.Len(); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		s, err := encodeElem(rv.Index(i))
		if err != nil {
			return "", err
		}
		writeArrayElem(&b, s)
	}
	b.WriteByte('}')
	return b.String(), nil
}

func encodeElem(v reflect.Value) (string, error) {
	// An element of a []any carries its type dynamically; unwrap to it, so a
	// string in an interface encodes as a string rather than failing here.
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "", fmt.Errorf("sqlb: a Postgres array element cannot be nil; NULL elements have no declaration in the schema")
		}
		v = v.Elem()
	}
	t := v.Type()
	if t == timeType {
		tv, ok := v.Interface().(time.Time)
		if !ok {
			return "", fmt.Errorf("sqlb: array element of type %s is not a time.Time", t)
		}
		return tv.Format(time.RFC3339Nano), nil
	}
	if t.Implements(textMarshalerType) {
		m, ok := v.Interface().(encoding.TextMarshaler)
		if !ok {
			return "", fmt.Errorf("sqlb: array element of type %s does not marshal to text", t)
		}
		text, err := m.MarshalText()
		if err != nil {
			return "", fmt.Errorf("sqlb: encoding array element of type %s: %w", t, err)
		}
		return string(text), nil
	}
	switch t.Kind() {
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		if v.Bool() {
			return "t", nil
		}
		return "f", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), nil
	case reflect.Float32:
		return strconv.FormatFloat(v.Float(), 'g', -1, 32), nil
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64), nil
	}
	return "", fmt.Errorf("sqlb: %s has no Postgres array element spelling", t)
}

// writeArrayElem writes one already-stringified element, quoting it when the
// bare form would not read back as itself.
//
// The empty string is the case that decides the rule: `{}` is the empty array
// and `{""}` is an array holding one empty string, so an unquoted empty element
// would change the length of the array. NULL is quoted for the same reason —
// unquoted it is the SQL null, quoted it is the four characters.
func writeArrayElem(b *strings.Builder, s string) {
	if !needsQuoting(s) {
		b.WriteString(s)
		return
	}
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
}

func needsQuoting(s string) bool {
	if s == "" || strings.EqualFold(s, "null") {
		return true
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(pgSpace, s[i]) >= 0 {
			return true
		}
		switch s[i] {
		case '{', '}', ',', '"', '\\':
			return true
		}
	}
	return false
}

// pgSpace is the whitespace Postgres trims from an unquoted array element.
//
// It is the ASCII set and not Unicode's, which is the distinction that matters:
// strings.TrimSpace also strips U+0085 and U+00A0, so a tag containing one
// would encode unquoted and read back a byte shorter. The encoder quotes
// exactly this set and the parser trims exactly this set, so the two stay
// symmetric — a property the fuzz target checks.
const pgSpace = " \t\n\r\v\f"

func trimPGSpace(s string) string { return strings.Trim(s, pgSpace) }

// parseArrayLiteral splits a Postgres array literal into its elements, marking
// which of them were the unquoted word NULL.
//
// One dimension only. A literal with a nested brace is refused rather than
// flattened: sqlb declares no two-dimensional column, so a nested one arriving
// here means the Go type and the database disagree, and silently reading the
// inner values would hide that.
func parseArrayLiteral(src string) (elems []string, nulls []bool, err error) {
	s := trimPGSpace(src)
	// A literal may carry an explicit dimension prefix, e.g. [0:2]={a,b,c},
	// which arrives from a column whose array does not start at index 1.
	if strings.HasPrefix(s, "[") {
		if end := strings.Index(s, "="); end >= 0 {
			s = trimPGSpace(s[end+1:])
		}
	}
	if s == "" {
		return nil, nil, nil
	}
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, nil, fmt.Errorf("sqlb: %q is not a Postgres array literal", src)
	}
	body := s[1 : len(s)-1]
	if trimPGSpace(body) == "" {
		return []string{}, []bool{}, nil
	}

	var (
		cur    strings.Builder
		quoted bool // this element arrived quoted, so NULL is the word
		inside bool // the cursor is between quotes
	)
	flush := func() {
		text := cur.String()
		if !quoted {
			text = trimPGSpace(text)
		}
		elems = append(elems, text)
		nulls = append(nulls, !quoted && strings.EqualFold(text, "null"))
		cur.Reset()
		quoted = false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case inside:
			if c == '\\' {
				if i+1 >= len(body) {
					return nil, nil, fmt.Errorf("sqlb: array literal %q ends in a backslash", src)
				}
				i++
				cur.WriteByte(body[i])
				continue
			}
			if c == '"' {
				inside = false
				continue
			}
			cur.WriteByte(c)
		case c == '"':
			inside, quoted = true, true
		case c == ',':
			flush()
		case c == '{' || c == '}':
			return nil, nil, fmt.Errorf("sqlb: array literal %q is multi-dimensional, which sqlb does not declare", src)
		default:
			cur.WriteByte(c)
		}
	}
	if inside {
		return nil, nil, fmt.Errorf("sqlb: array literal %q has an unterminated quoted element", src)
	}
	flush()
	return elems, nulls, nil
}

// arrayScanner points a slice field at a result column holding an array.
//
// It is the sql.Scanner the scan loop substitutes for the field's own address,
// so the array codec sits in the one place a result column is assigned to a
// struct field and nowhere else.
type arrayScanner struct {
	dest reflect.Value // the addressable slice field
	col  string        // named in errors, since the model has many
}

func (a arrayScanner) Scan(src any) error {
	if src == nil {
		a.dest.Set(reflect.Zero(a.dest.Type()))
		return nil
	}
	var text string
	switch v := src.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("sqlb: column %q holds %T, which is not a Postgres array literal", a.col, src)
	}

	elems, nulls, err := parseArrayLiteral(text)
	if err != nil {
		return fmt.Errorf("sqlb: column %q: %w", a.col, err)
	}
	if elems == nil {
		a.dest.Set(reflect.Zero(a.dest.Type()))
		return nil
	}

	out := reflect.MakeSlice(a.dest.Type(), len(elems), len(elems))
	for i := range elems {
		if nulls[i] {
			// The declaration has no way to say elements may be NULL, so one
			// arriving means the column is not the one the model describes.
			// Zeroing it would put an empty string where a missing value was.
			return fmt.Errorf("sqlb: column %q holds a NULL element at position %d, which the model's %s cannot represent", a.col, i+1, a.dest.Type())
		}
		if err := decodeElem(out.Index(i), elems[i]); err != nil {
			return fmt.Errorf("sqlb: column %q element %d: %w", a.col, i+1, err)
		}
	}
	a.dest.Set(out)
	return nil
}

func decodeElem(dest reflect.Value, s string) error {
	t := dest.Type()
	if t == timeType {
		for _, layout := range pgTimeLayouts {
			if v, err := time.Parse(layout, s); err == nil {
				dest.Set(reflect.ValueOf(v))
				return nil
			}
		}
		return fmt.Errorf("%q is not a timestamp, date or time", s)
	}
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		v := reflect.New(t)
		u, ok := v.Interface().(encoding.TextUnmarshaler)
		if !ok {
			return fmt.Errorf("%s does not unmarshal from text", t)
		}
		if err := u.UnmarshalText([]byte(s)); err != nil {
			return fmt.Errorf("invalid %s value %q: %w", t, s, err)
		}
		dest.Set(v.Elem())
		return nil
	}
	switch t.Kind() {
	case reflect.String:
		dest.SetString(s)
		return nil
	case reflect.Bool:
		// Postgres writes t and f inside an array literal, not true and false.
		switch s {
		case "t", "true", "TRUE", "T":
			dest.SetBool(true)
			return nil
		case "f", "false", "FALSE", "F":
			dest.SetBool(false)
			return nil
		}
		return fmt.Errorf("%q is not a boolean", s)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, t.Bits())
		if err != nil {
			return fmt.Errorf("%q is not an integer that fits %s", s, t)
		}
		dest.SetInt(n)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, t.Bits())
		if err != nil {
			return fmt.Errorf("%q is not a non-negative integer that fits %s", s, t)
		}
		dest.SetUint(n)
		return nil
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, t.Bits())
		if err != nil {
			return fmt.Errorf("%q is not a number", s)
		}
		dest.SetFloat(f)
		return nil
	}
	return fmt.Errorf("%s cannot be read from a Postgres array element", t)
}

// pgTimeLayouts are the spellings Postgres writes inside an array literal for
// the three time types. The offset form comes back as +02 rather than +02:00,
// which is not RFC 3339 and so needs its own layout.
var pgTimeLayouts = []string{
	"2006-01-02 15:04:05.999999-07",
	"2006-01-02 15:04:05.999999-07:00",
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999",
	"2006-01-02",
	"15:04:05.999999",
}

// arrayParam is the driver.Valuer a bound slice becomes.
//
// Binding the literal as a string rather than converting at the call site keeps
// the conversion lazy, so a statement that is compiled and never executed —
// SQL(), or a plan under Explain — costs nothing for it. An encoding failure
// surfaces where the driver reports it, with the statement already attached.
type arrayParam struct{ v any }

func (a arrayParam) Value() (driver.Value, error) {
	// A nil slice is NULL and an empty one is {}. They are different values in
	// Postgres and the Go side has two spellings for them, so writing both as
	// the empty array would lose a distinction the scan side preserves — and
	// silently turn every unset nullable array column into an empty one.
	rv := reflect.ValueOf(a.v)
	if rv.IsValid() && rv.Kind() == reflect.Slice && rv.IsNil() {
		return nil, nil
	}
	s, err := EncodeArray(a.v)
	if err != nil {
		return nil, err
	}
	return s, nil
}

var (
	timeType            = reflect.TypeOf(time.Time{})
	textMarshalerType   = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()

	_ driver.Valuer = arrayParam{}
	_ sql.Scanner   = arrayScanner{}
)
