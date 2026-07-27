package filter

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Error is one rejected query parameter.
//
// It carries the allowed alternatives where there are any, because the caller
// most likely to read it is a program assembling requests against a schema it
// only partly knows. "column is not sortable" is a dead end; the same message
// plus the sortable columns is a fix.
type Error struct {
	Param   string   `json:"param"`
	Value   string   `json:"value,omitempty"`
	Reason  string   `json:"reason"`
	Allowed []string `json:"allowed,omitempty"`
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Param)
	if e.Value != "" {
		b.WriteString("=")
		b.WriteString(e.Value)
	}
	b.WriteString(": ")
	b.WriteString(e.Reason)
	if len(e.Allowed) > 0 {
		b.WriteString(" (allowed: ")
		b.WriteString(strings.Join(e.Allowed, ", "))
		b.WriteString(")")
	}
	return b.String()
}

// Errors is the set of problems found in one request. Parsing collects them
// all rather than stopping at the first, so a malformed request needs one
// round trip to fix rather than one per mistake.
type Errors []*Error

func (e Errors) Error() string {
	switch len(e) {
	case 0:
		return "filter: no error"
	case 1:
		return "filter: " + e[0].Error()
	}
	parts := make([]string, len(e))
	for i, err := range e {
		parts[i] = err.Error()
	}
	return "filter: " + strings.Join(parts, "; ")
}

// AsErrors extracts parse errors from err, unwrapping as it goes.
//
// Prefer it to a type assertion. Parse returns Errors directly today, but a
// hook, a middleware or a caller adding context will wrap it, and
// `err.(filter.Errors)` panics the moment that happens:
//
//	if errs, ok := filter.AsErrors(err); ok {
//	    errs.WriteHTTP(w)
//	    return
//	}
func AsErrors(err error) (Errors, bool) {
	var errs Errors
	if errors.As(err, &errs) {
		return errs, true
	}
	return nil, false
}

// WriteError writes err as a JSON problem response if it is a parse failure,
// and reports whether it did. It is the whole error path of a list handler.
func WriteError(w http.ResponseWriter, err error) bool {
	errs, ok := AsErrors(err)
	if !ok {
		return false
	}
	errs.WriteHTTP(w)
	return true
}

// StatusCode is 400: every parse failure is a malformed request.
func (e Errors) StatusCode() int { return http.StatusBadRequest }

// WriteHTTP writes the errors as a JSON problem response.
func (e Errors) WriteHTTP(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(e.StatusCode())
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":   "invalid_query",
		"message": "one or more query parameters were rejected",
		"details": []*Error(e),
	})
}
