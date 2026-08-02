package schema

import "strings"

// Rendering an exclusion, and reading one back.
//
// These two are a pair and live together on purpose. The declared side is
// rendered by Def; the introspected side and the normalised side are both
// produced by ParseExclusion over what pg_get_constraintdef returns. So the two
// sides of a diff agree by construction rather than because two packages were
// written to the same guess — which is the failure mode the enum constraint
// name hid in for as long as it did, where both sides dropped the same thing
// identically and every registry-level check passed.

// Def renders the constraint body: everything a CREATE TABLE or an ALTER TABLE
// writes after CONSTRAINT <name>.
func (e Exclusion) Def() string {
	var b strings.Builder
	b.WriteString("EXCLUDE ")
	if e.Using != "" {
		b.WriteString("USING " + e.Using + " ")
	}
	b.WriteString("(" + e.Elements + ")")
	if e.Where != "" {
		b.WriteString(" WHERE (" + e.Where + ")")
	}
	return b.String()
}

// ParseExclusion splits what pg_get_constraintdef returns for an EXCLUDE
// constraint into its parts.
//
// The grammar it accepts is the one Postgres emits, which is fixed:
//
//	EXCLUDE USING gist (coach_id WITH =, tstzrange(starts_at, ends_at) WITH &&) WHERE (...)
//
// Parsing rather than storing the whole string is what lets the parts be read
// and edited in a declaration. It is safe to parse because the input is always
// Postgres's own output — both on the introspect path and on the normalise
// path, which probes the declared spelling by adding the real constraint and
// reading it back. A definition this cannot parse returns false rather than a
// half-filled Exclusion, and the caller reports it as unrepresentable, which is
// the same contract every other construct here has.
func ParseExclusion(def string) (Exclusion, bool) {
	rest := strings.TrimSpace(def)
	const prefix = "EXCLUDE"
	if !strings.HasPrefix(rest, prefix) {
		return Exclusion{}, false
	}
	rest = strings.TrimSpace(rest[len(prefix):])

	var e Exclusion
	if after, ok := strings.CutPrefix(rest, "USING "); ok {
		method, remainder, found := strings.Cut(strings.TrimSpace(after), "(")
		if !found {
			return Exclusion{}, false
		}
		e.Using = strings.TrimSpace(method)
		if e.Using == "" {
			return Exclusion{}, false
		}
		rest = "(" + remainder
	}

	elements, rest, ok := cutBalanced(rest)
	if !ok {
		return Exclusion{}, false
	}
	e.Elements = strings.TrimSpace(elements)
	if e.Elements == "" {
		return Exclusion{}, false
	}

	rest = strings.TrimSpace(rest)
	if rest == "" {
		return e, true
	}
	after, ok := strings.CutPrefix(rest, "WHERE ")
	if !ok {
		// Something follows the element list that this does not model —
		// DEFERRABLE, INCLUDE, WITH (storage parameters). Refused rather than
		// dropped, because a constraint imported without a clause it carries is
		// one whose next diff proposes replacing it (ADR-0014).
		return Exclusion{}, false
	}
	where, tail, ok := cutBalanced(strings.TrimSpace(after))
	if !ok || strings.TrimSpace(tail) != "" {
		return Exclusion{}, false
	}
	e.Where = strings.TrimSpace(where)
	return e, true
}

// cutBalanced takes a string beginning with "(" and returns what is inside the
// matching close paren, plus whatever follows it.
//
// Balanced rather than "up to the last paren", because an element list contains
// them: tstzrange(starts_at, ends_at) is the ordinary case rather than an
// exotic one. Quotes are tracked so that a paren or a quote inside a literal —
// WHERE (status = 'a)b') — does not end the group early.
func cutBalanced(s string) (inside, rest string, ok bool) {
	if !strings.HasPrefix(s, "(") {
		return "", "", false
	}
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			// Postgres doubles an embedded quote rather than escaping it, so a
			// doubled one reads as a close immediately followed by an open and
			// needs no special case.
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[1:i], s[i+1:], true
			}
		}
	}
	return "", "", false
}
