package shadow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// file is one migration's forward direction, ready to apply.
type file struct {
	Name    string // base name, for diagnostics
	Version int64
	// Statements are the forward statements in order.
	Statements []string
	// NoTransaction reports that the file asked not to be wrapped in one,
	// because it contains something that cannot run inside a transaction.
	NoTransaction bool
}

// collect reads a migration directory and returns the forward direction of
// every migration in it, ordered by version.
func collect(dir, format string) ([]file, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("shadow: reading %s: %w", dir, err)
	}

	var files []file
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		// golang-migrate writes a pair per version; only the forward half is
		// replayed, and the reverse half must not be mistaken for one.
		if strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}

		version, err := versionOf(e.Name())
		if err != nil {
			return nil, err
		}

		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("shadow: reading %s: %w", e.Name(), err)
		}

		f := file{Name: e.Name(), Version: version}
		body := string(raw)
		if format == "goose" {
			f.NoTransaction = hasGooseDirective(body, "NO TRANSACTION")
			body, err = gooseUp(e.Name(), body)
			if err != nil {
				return nil, err
			}
		}
		f.Statements = splitStatements(body)
		// A statement that cannot run inside a transaction decides how the
		// file runs, whatever the format. goose says so with a directive;
		// golang-migrate and plain SQL have no way to, and migrate.Unblock
		// emits CREATE INDEX CONCURRENTLY for all three — so without this,
		// shadow could not replay the histories this repository itself
		// generates for two of the formats it supports.
		if !f.NoTransaction && anyConcurrent(f.Statements) {
			f.NoTransaction = true
		}
		files = append(files, f)
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].Version != files[j].Version {
			return files[i].Version < files[j].Version
		}
		return files[i].Name < files[j].Name
	})

	for i := 1; i < len(files); i++ {
		if files[i].Version == files[i-1].Version {
			return nil, fmt.Errorf(
				"shadow: %s and %s share version %d; the order they applied in is not "+
					"recorded anywhere, so replaying them cannot be faithful",
				files[i-1].Name, files[i].Name, files[i].Version)
		}
	}
	return files, nil
}

// versionOf reads the leading version from a migration filename.
//
// Parsed as a number rather than sorted as text, because sequential versions
// sort wrongly as text — "10" lands before "2" — and a migration history
// applied in the wrong order produces a schema that never existed.
func versionOf(name string) (int64, error) {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".sql"), ".up")
	digits := base
	if i := strings.IndexByte(base, '_'); i >= 0 {
		digits = base[:i]
	}
	v, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf(
			"shadow: %s does not start with a numeric version, so it cannot be "+
				"ordered against the others; expected <version>_<name>.sql", name)
	}
	return v, nil
}

// gooseUp extracts the Up section of a goose migration.
//
// Everything from "-- +goose Up" to "-- +goose Down" (or the end of the file).
// StatementBegin/End markers are dropped once their contents have been kept
// together — see splitStatements for why they cannot simply be ignored.
func gooseUp(name, body string) (string, error) {
	lines := strings.Split(body, "\n")
	var out []string
	inUp := false
	for _, line := range lines {
		switch directive(line) {
		case "Up":
			inUp = true
			continue
		case "Down":
			inUp = false
			continue
		case "StatementBegin", "StatementEnd":
			// Kept as markers so splitStatements can honour them, but only
			// inside the section being collected.
			if inUp {
				out = append(out, line)
			}
			continue
		}
		if inUp {
			out = append(out, line)
		}
	}
	if !inUp && len(out) == 0 {
		return "", fmt.Errorf(
			"shadow: %s has no `-- +goose Up` section, so there is nothing to replay. "+
				"If this directory is not goose format, set Options.Format", name)
	}
	return strings.Join(out, "\n"), nil
}

// directive returns the goose directive on a line, or "".
func directive(line string) string {
	t := strings.TrimSpace(line)
	const prefix = "-- +goose "
	if !strings.HasPrefix(t, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(t, prefix))
}

func hasGooseDirective(body, want string) bool {
	for _, line := range strings.Split(body, "\n") {
		if directive(line) == want {
			return true
		}
	}
	return false
}

// splitStatements splits SQL into individual statements on top-level
// semicolons.
//
// Splitting is not optional. A file that asked not to be wrapped in a
// transaction contains something that cannot run inside one — CREATE INDEX
// CONCURRENTLY — and Postgres wraps a multi-statement simple query in an
// implicit transaction, so sending such a file whole would fail on the one
// statement the directive exists for.
//
// A semicolon does not end a statement when it is inside a string literal, a
// dollar-quoted body, or a comment. A string literal has two spellings: the
// ordinary one, where a doubled quote is an escaped quote, and Postgres's
// escape-string form E'…', where a backslash escapes the character after it —
// so E'it\'s done; almost' is one literal and splitting it at the backslashed
// quote produces two broken halves. Those are the cases; a goose
// StatementBegin block is kept whole regardless, because that is exactly what
// the marker is for: a function body whose semicolons are not statement
// boundaries.
func splitStatements(sql string) []string {
	var out []string
	for _, seg := range segments(sql) {
		if seg.whole {
			if s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(seg.text), ";")); s != "" {
				out = append(out, s)
			}
			continue
		}
		out = append(out, scanSplit(seg.text)...)
	}
	return out
}

// segment is a run of the file that is either kept whole — a goose
// StatementBegin block — or split on semicolons like ordinary SQL.
type segment struct {
	text  string
	whole bool
}

// segments splits a file on goose's StatementBegin/StatementEnd markers,
// preserving the order of what lies between them.
func segments(sql string) []segment {
	var out []segment
	var cur strings.Builder
	inBlock := false

	flush := func(whole bool) {
		if strings.TrimSpace(cur.String()) != "" {
			out = append(out, segment{text: cur.String(), whole: whole})
		}
		cur.Reset()
	}

	for _, line := range strings.Split(sql, "\n") {
		switch directive(line) {
		case "StatementBegin":
			flush(false)
			inBlock = true
			continue
		case "StatementEnd":
			flush(true)
			inBlock = false
			continue
		}
		cur.WriteString(line + "\n")
	}
	flush(inBlock)
	return out
}

// scanSplit splits ordinary SQL on semicolons that actually end a statement.
//
// One pass, four states. Anything else Postgres accepts here — identifiers,
// numbers, operators — cannot contain a semicolon, so it needs no state of its
// own.
func scanSplit(sql string) []string {
	var out []string
	var cur strings.Builder

	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}

	for i := 0; i < len(sql); {
		switch {
		case sql[i] == '\'' || isEscapeStringPrefix(sql, i):
			// A single-quoted literal. Two quotes in a row is an escaped quote
			// and does not end it. In the E'…' form a backslash also escapes
			// whatever follows, including a quote.
			escapes := sql[i] != '\''
			if escapes {
				// Consume the E, leaving i on the opening quote.
				cur.WriteByte(sql[i])
				i++
			}
			cur.WriteByte(sql[i])
			i++
			for i < len(sql) {
				if escapes && sql[i] == '\\' && i+1 < len(sql) {
					cur.WriteByte(sql[i])
					cur.WriteByte(sql[i+1])
					i += 2
					continue
				}
				if sql[i] == '\'' {
					if i+1 < len(sql) && sql[i+1] == '\'' {
						cur.WriteString("''")
						i += 2
						continue
					}
					cur.WriteByte('\'')
					i++
					break
				}
				cur.WriteByte(sql[i])
				i++
			}

		case sql[i] == '"':
			// A quoted identifier. Cannot contain a semicolon that matters,
			// but can contain one, so it still has to be skipped over.
			cur.WriteByte(sql[i])
			i++
			for i < len(sql) {
				cur.WriteByte(sql[i])
				if sql[i] == '"' {
					i++
					break
				}
				i++
			}

		case sql[i] == '$':
			if tag, ok := dollarTag(sql[i:]); ok {
				end := strings.Index(sql[i+len(tag):], tag)
				if end < 0 {
					// Unterminated: emit the rest rather than losing it, and
					// let the database report what is wrong with it.
					cur.WriteString(sql[i:])
					i = len(sql)
					break
				}
				stop := i + len(tag) + end + len(tag)
				cur.WriteString(sql[i:stop])
				i = stop
				break
			}
			cur.WriteByte(sql[i])
			i++

		case strings.HasPrefix(sql[i:], "--"):
			for i < len(sql) && sql[i] != '\n' {
				cur.WriteByte(sql[i])
				i++
			}

		case strings.HasPrefix(sql[i:], "/*"):
			// Postgres block comments nest.
			depth := 0
			for i < len(sql) {
				if strings.HasPrefix(sql[i:], "/*") {
					depth++
					cur.WriteString("/*")
					i += 2
					continue
				}
				if strings.HasPrefix(sql[i:], "*/") {
					depth--
					cur.WriteString("*/")
					i += 2
					if depth == 0 {
						break
					}
					continue
				}
				cur.WriteByte(sql[i])
				i++
			}

		case sql[i] == ';':
			flush()
			i++

		default:
			cur.WriteByte(sql[i])
			i++
		}
	}
	flush()
	return out
}

// dollarTag returns the dollar-quote tag starting at s, if there is one:
// "$$" or "$name$". A bare "$" followed by digits is a positional parameter
// and is not one.
func dollarTag(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '$' {
			return s[:i+1], true
		}
		if s[i] != '_' && (s[i] < 'a' || s[i] > 'z') && (s[i] < 'A' || s[i] > 'Z') &&
			(i <= 1 || s[i] < '0' || s[i] > '9') {
			return "", false
		}
	}
	return "", false
}

// isEscapeStringPrefix reports whether sql[i] opens an E'…' escape string.
//
// The E is only a prefix when it is not part of a longer word: `type E'x'` is
// a literal, and the E of `CASE'` is not. A preceding identifier character
// therefore rules it out.
func isEscapeStringPrefix(sql string, i int) bool {
	if sql[i] != 'E' && sql[i] != 'e' {
		return false
	}
	if i+1 >= len(sql) || sql[i+1] != '\'' {
		return false
	}
	if i == 0 {
		return true
	}
	switch c := sql[i-1]; {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		return false
	}
	return true
}

// anyConcurrent reports whether a statement must run outside a transaction.
//
// CONCURRENTLY is the whole list: Postgres rejects a concurrent index build
// inside a transaction block, and nothing else the DDL layer emits has that
// property.
func anyConcurrent(stmts []string) bool {
	for _, s := range stmts {
		if strings.Contains(strings.ToUpper(s), "CONCURRENTLY") {
			return true
		}
	}
	return false
}
