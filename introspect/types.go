package introspect

import (
	"strconv"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// columnType maps what format_type reports onto a logical type.
//
// The spellings are Postgres's canonical ones rather than the ones the DDL
// layer emits: a column declared varchar(200) reports as "character
// varying(200)", timestamptz as "timestamp with time zone", and time as "time
// without time zone". Writing this against the emitted spellings would have
// matched almost nothing.
//
// ok is false for a type the DSL has no equivalent for, which is reported
// rather than guessed at — a column silently imported as text would produce a
// migration proposing to change the real column's type to text.
func columnType(formatted string) (t schema.Type, size int, ok bool) {
	base, arg := splitTypeArg(formatted)
	switch base {
	case "text":
		return schema.TypeText, 0, true
	case "character varying":
		// A varchar with no length is indistinguishable from text in
		// Postgres, and the DDL layer renders both as text, so importing it
		// as text keeps the round trip closed.
		if arg == "" {
			return schema.TypeText, 0, true
		}
		n, err := strconv.Atoi(arg)
		if err != nil {
			return "", 0, false
		}
		return schema.TypeVarchar, n, true
	case "integer", "smallint":
		// smallint widens to integer, which is the safe direction: the DSL
		// cannot express it, and a migration from this schema would widen the
		// column rather than narrow it.
		return schema.TypeInt, 0, base == "integer"
	case "bigint":
		return schema.TypeBigInt, 0, true
	case "double precision", "real":
		return schema.TypeFloat, 0, base == "double precision"
	case "numeric":
		// A numeric with a precision is a different type from an unbounded
		// one and the DSL has no way to say so.
		return schema.TypeNumeric, 0, arg == ""
	case "boolean":
		return schema.TypeBool, 0, true
	case "uuid":
		return schema.TypeUUID, 0, true
	case "timestamp with time zone":
		return schema.TypeTimestamp, 0, true
	case "date":
		return schema.TypeDate, 0, true
	case "time without time zone":
		return schema.TypeTime, 0, true
	case "jsonb":
		return schema.TypeJSON, 0, true
	case "bytea":
		return schema.TypeBytes, 0, true
	}
	return "", 0, false
}

// splitTypeArg splits "character varying(200)" into its name and its argument.
func splitTypeArg(s string) (base, arg string) {
	open := strings.IndexByte(s, '(')
	if open < 0 || !strings.HasSuffix(s, ")") {
		return s, ""
	}
	return s[:open], s[open+1 : len(s)-1]
}

// The generators the schema package ships, recognised by the exact text they
// produce. Anything else becomes a raw expression, which is faithful whether or
// not this package understands it.
var knownDefaults = map[string]func() *schema.Default{
	"now()":              schema.Now,
	"uuid_generate_v7()": schema.GenUUIDv7,
	"gen_random_uuid()":  schema.GenUUIDv4,
	"CURRENT_TIMESTAMP":  schema.Now,
}

// columnDefault maps a stored default expression back onto a schema.Default.
//
// Postgres attaches a cast to every literal it stores — 'draft' comes back as
// 'draft'::text — so a redundant cast, one naming the column's own type, is
// stripped. That is not cosmetic: it makes the default render as the DDL layer
// would have written it, so a schema imported and then diffed against itself
// produces nothing.
//
// A cast to anything else is left alone inside a raw expression, because it is
// doing something, and this package has no business deciding what.
func columnDefault(expr, formatted string, t schema.Type) *schema.Default {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}
	if gen, known := knownDefaults[expr]; known {
		return gen()
	}
	if lit, ok := stripCast(expr, formatted); ok {
		if s, quoted := unquoteLiteral(lit); quoted {
			return schema.Value(s)
		}
		return schema.Expr(lit)
	}
	// Bare numbers and booleans carry no cast and render identically either
	// way, so they pass through as expressions rather than being parsed into
	// Go values whose type would then have to be guessed.
	return schema.Expr(expr)
}

// stripCast removes a trailing ::type when it names the column's own type, and
// reports whether it did.
func stripCast(expr, formatted string) (string, bool) {
	cut := strings.LastIndex(expr, "::")
	if cut < 0 {
		return expr, false
	}
	if strings.TrimSpace(expr[cut+2:]) != formatted {
		return expr, false
	}
	return strings.TrimSpace(expr[:cut]), true
}

// unquoteLiteral turns a SQL string literal into its value.
func unquoteLiteral(s string) (string, bool) {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return "", false
	}
	return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), true
}

// enumValues recovers the permitted values of an enum column from the CHECK
// that enforces it.
//
// The DDL layer writes `"status" IN ('draft', 'live')`, and Postgres stores the
// normalised form `status = ANY (ARRAY['draft'::text, 'live'::text])`. Matching
// the form that was written would have recovered nothing; this matches the form
// that comes back.
//
// Enums are text with a CHECK rather than a native Postgres enum (ADR-0017),
// which is what makes recovering them a matter of reading an expression rather
// than reading a type.
func enumValues(column, expr string) ([]string, bool) {
	expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(expr), "("), ")"))
	prefix := column + " = ANY (ARRAY["
	if !strings.HasPrefix(expr, prefix) || !strings.HasSuffix(expr, "])") {
		return nil, false
	}
	body := expr[len(prefix) : len(expr)-len("])")]
	if strings.TrimSpace(body) == "" {
		return nil, false
	}
	var out []string
	for _, part := range splitTopLevel(body) {
		lit, _ := stripCastAny(strings.TrimSpace(part))
		v, quoted := unquoteLiteral(lit)
		if !quoted {
			return nil, false
		}
		out = append(out, v)
	}
	return out, len(out) > 0
}

// stripCastAny removes a trailing cast whatever it names.
func stripCastAny(s string) (string, bool) {
	cut := strings.LastIndex(s, "::")
	if cut < 0 {
		return s, false
	}
	return strings.TrimSpace(s[:cut]), true
}

// splitTopLevel splits on commas that are not inside a string literal, so that
// a permitted value containing a comma survives.
func splitTopLevel(s string) []string {
	var out []string
	var cur strings.Builder
	inString := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\'' && i+1 < len(s) && s[i+1] == '\'' && inString:
			cur.WriteString("''")
			i++
		case s[i] == '\'':
			inString = !inString
			cur.WriteByte(s[i])
		case s[i] == ',' && !inString:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

// splitList splits a comma-joined column list from the catalog queries.
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// referentialAction maps confdeltype and confupdtype onto the DSL's actions.
// "a" is NO ACTION, which the DDL layer omits because it is the default.
func referentialAction(code string) (schema.Action, bool) {
	switch code {
	case "a", "":
		return schema.NoAction, true
	case "c":
		return schema.Cascade, true
	case "n":
		return schema.SetNull, true
	case "d":
		return schema.SetDefault, true
	case "r":
		return schema.Restrict, true
	}
	return schema.NoAction, false
}
