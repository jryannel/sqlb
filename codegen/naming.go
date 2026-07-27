package codegen

import (
	"strings"
	"unicode"
)

// initialisms are rendered upper-case in Go identifiers, following the
// convention the standard library and every Go linter expect: org_id becomes
// OrgID rather than OrgId.
var initialisms = map[string]string{
	"id": "ID", "url": "URL", "uri": "URI", "api": "API", "db": "DB",
	"http": "HTTP", "https": "HTTPS", "json": "JSON", "xml": "XML",
	"sql": "SQL", "uuid": "UUID", "ip": "IP", "cpu": "CPU", "ttl": "TTL",
	"acl": "ACL", "ssh": "SSH", "tls": "TLS", "ui": "UI", "eof": "EOF",
}

// GoName converts a snake_case SQL identifier to an exported Go name.
//
//	org_id        → OrgID
//	created_at    → CreatedAt
//	password_hash → PasswordHash
func GoName(s string) string {
	var b strings.Builder
	for _, part := range strings.Split(s, "_") {
		if part == "" {
			continue
		}
		if up, ok := initialisms[strings.ToLower(part)]; ok {
			b.WriteString(up)
			continue
		}
		r := []rune(part)
		b.WriteRune(unicode.ToUpper(r[0]))
		b.WriteString(string(r[1:]))
	}
	return b.String()
}

// unexportedGoName is GoName with a lower-case first letter, for the private
// column-set types. It avoids producing a Go keyword.
func unexportedGoName(s string) string {
	name := GoName(s)
	if name == "" {
		return name
	}
	r := []rune(name)
	// A leading initialism must be lowered whole, or ID becomes iD.
	lead := 0
	for lead < len(r) && unicode.IsUpper(r[lead]) {
		lead++
	}
	if lead > 1 && lead < len(r) {
		lead-- // the last upper-case rune starts the next word
	}
	for i := 0; i < lead; i++ {
		r[i] = unicode.ToLower(r[i])
	}
	out := string(r)
	if isGoKeyword(out) {
		return out + "_"
	}
	return out
}

func isGoKeyword(s string) bool {
	switch s {
	case "break", "case", "chan", "const", "continue", "default", "defer", "else",
		"fallthrough", "for", "func", "go", "goto", "if", "import", "interface",
		"map", "package", "range", "return", "select", "struct", "switch", "type", "var":
		return true
	}
	return false
}

// Singular is a deliberately small English singulariser, the inverse of the
// pluraliser in the runtime.
//
// It only has to produce a readable Go type name: correctness does not depend
// on it, because every generated model carries an explicit TableName method, so
// a wrong guess is cosmetic rather than a mapping bug.
func Singular(s string) string {
	lower := strings.ToLower(s)
	switch {
	case strings.HasSuffix(lower, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(lower, "ches"), strings.HasSuffix(lower, "shes"),
		strings.HasSuffix(lower, "xes"), strings.HasSuffix(lower, "zes"),
		strings.HasSuffix(lower, "sses"):
		return s[:len(s)-2]
	case strings.HasSuffix(lower, "ss"):
		return s // "address" is already singular
	case strings.HasSuffix(lower, "s") && !strings.HasSuffix(lower, "us"):
		return s[:len(s)-1]
	}
	return s
}

// TypeName is the Go type name for a table: the singular of its local name,
// exported. A module prefix is deliberately not included — billing_invoices
// yields Invoice, because the prefix is a storage concern and the package
// already provides the namespace in Go.
func TypeName(localName string) string {
	return GoName(Singular(localName))
}
