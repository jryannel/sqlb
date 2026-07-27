package rest

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/filter"
)

// Problem is the body of every rejection this package produces.
//
// It is RFC 9457 shaped, like Huma's own, so a generated client sees one error
// type across the whole API. The one addition is `allowed` on each detail,
// which carries what the caller could have asked for instead — the substance of
// ADR-0011. Huma's own ErrorDetail has no room for it, and flattening the
// allow-list into the message would leave a client parsing prose to recover.
//
// A handler returning this value has it marshalled directly, because it
// satisfies huma.StatusError.
type Problem struct {
	// Type is the RFC 9457 problem type URI.
	Type string `json:"type,omitempty" doc:"A URI reference identifying the problem type"`
	// Title is the short, human-readable summary of the problem.
	Title string `json:"title,omitempty" doc:"Short, human-readable summary of the problem"`
	// Status is the HTTP status code.
	Status int `json:"status,omitempty" doc:"HTTP status code"`
	// Detail explains this specific occurrence.
	Detail string `json:"detail,omitempty" doc:"Explanation specific to this occurrence"`
	// Errors lists every problem found, not just the first, so a malformed
	// request takes one round trip to fix rather than one per mistake.
	Errors []*ProblemDetail `json:"errors,omitempty" doc:"Every problem found with the request"`
}

// ProblemDetail is one rejected parameter or field.
type ProblemDetail struct {
	// Message says what was wrong.
	Message string `json:"message" doc:"What was wrong"`
	// Location is a path-like pointer to the offending input, e.g.
	// `query.sort` or `body.title`.
	Location string `json:"location,omitempty" doc:"Where the problem is, e.g. 'query.sort'"`
	// Value is the rejected value, echoed back.
	Value any `json:"value,omitempty" doc:"The rejected value"`
	// Allowed lists what would have been accepted instead, where there is a
	// finite set. Hidden columns never appear here: the diagnostic must not
	// become an oracle for what a resource is concealing.
	Allowed []string `json:"allowed,omitempty" doc:"What would have been accepted instead"`
}

// Error satisfies the error interface.
func (e *Problem) Error() string {
	if len(e.Errors) == 0 {
		return e.Detail
	}
	return fmt.Sprintf("%s (%d problems)", e.Detail, len(e.Errors))
}

// GetStatus satisfies huma.StatusError, which is what makes Huma write this
// model rather than converting it to its own.
func (e *Problem) GetStatus() int { return e.Status }

// ContentType marks the body as an RFC 9457 problem document.
func (e *Problem) ContentType(string) string { return "application/problem+json" }

// newError builds a problem document with no per-field detail.
func newError(status int, detail string) *Problem {
	return &Problem{
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	}
}

// invalidQuery converts filter's parse failures into a problem document,
// preserving the allow-lists that make a rejection actionable.
//
// The status is 400 rather than Huma's usual 422 for validation, matching
// filter.Errors.StatusCode: these are malformed query parameters, and the
// resource has no way to represent them as a well-formed entity that failed
// semantic checks.
func invalidQuery(errs filter.Errors) *Problem {
	out := newError(http.StatusBadRequest, "one or more query parameters were rejected")
	for _, e := range errs {
		detail := &ProblemDetail{
			Message:  e.Reason,
			Location: "query." + e.Param,
			Allowed:  e.Allowed,
		}
		if e.Value != "" {
			detail.Value = e.Value
		}
		out.Errors = append(out.Errors, detail)
	}
	return out
}

// asHumaError maps an error from the engine onto a response.
//
// Only the cases the REST layer can classify are mapped. Anything else becomes
// a 500 with its text left to Huma, because a database error that this package
// does not recognise is a bug here or an outage there, and dressing it up as a
// 400 would send the client looking in the wrong place.
func asHumaError(err error, resource string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sqlb.ErrNotFound):
		return newError(http.StatusNotFound, fmt.Sprintf("no %s matched", resource))
	}
	if errs, ok := filter.AsErrors(err); ok {
		return invalidQuery(errs)
	}
	return err
}

// errorResponses documents the failures an operation can produce.
//
// They are set on the Operation rather than left to Huma's Errors field,
// because Huma would document its own error model there and this package
// answers with a different one.
func errorResponses(reg huma.Registry, codes ...int) map[string]*huma.Response {
	schema := reg.Schema(reflect.TypeFor[Problem](), true, "Problem")
	out := make(map[string]*huma.Response, len(codes))
	for _, code := range codes {
		out[fmt.Sprint(code)] = &huma.Response{
			Description: http.StatusText(code),
			Content: map[string]*huma.MediaType{
				"application/problem+json": {Schema: schema},
			},
		}
	}
	return out
}
