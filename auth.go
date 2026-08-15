package sqlb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// CredentialExtractor pulls a raw credential — a bearer token, a cookie
// value, whatever a provider issues — out of an inbound request. ok is
// false when no credential is present, which Middleware treats as a
// missing-credential rejection rather than an invalid one.
type CredentialExtractor func(r *http.Request) (cred string, ok bool)

// BearerToken extracts a credential from the Authorization: Bearer <token>
// header (RFC 6750). It is the default extractor: Zitadel's OIDC access
// tokens, self-hosted JWTs, and WorkOS/Clerk in API mode all present this
// way. A provider whose browser flow hands back a cookie instead (WorkOS's
// AuthKit, Clerk's hosted UI) needs its own CredentialExtractor — Middleware
// takes the extractor as a parameter rather than hardcoding this one so that
// substitution costs nothing.
func BearerToken(r *http.Request) (string, bool) {
	const prefix = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}

// Verifier checks a credential and returns the application's own principal
// type. T is the same type the application later reads back with
// PrincipalFrom[T] — Verifier does not introduce a new principal shape, it
// produces the one the application already owns.
//
// Different providers hand back different claim shapes; Verifier stays
// generic over T rather than sqlb defining a canonical principal struct, so
// a WorkOS, Clerk, Zitadel, or self-hosted-JWT adapter maps its provider's
// claims into whatever type the application's hooks already read via
// PrincipalFrom[T].
type Verifier[T any] interface {
	Verify(ctx context.Context, cred string) (T, error)
}

// TransientError marks a Verify failure as not-a-verdict-on-the-credential —
// a network error reaching the provider, a provider 5xx, a timeout — so
// Middleware answers 500 instead of 401. "The provider is down" and "the
// token is bad" are different failures for both an operator paging on 5xx
// and a client that should not retry a rejected credential; collapsing them
// into one status code erases that distinction.
//
// This is opt-in. A Verifier with no network call to fail — local JWT
// verification, for instance — never has a transient failure mode and never
// needs to return one; every error it returns is correctly a 401.
type TransientError struct{ Err error }

func (e TransientError) Error() string { return e.Err.Error() }
func (e TransientError) Unwrap() error { return e.Err }

// Middleware wraps a Verifier[T] as net/http middleware: extract the
// credential, verify it, and on success carry the resulting principal on
// the request context via WithPrincipal before calling next. It is
// ordinary net/http middleware, not anything huma-specific, so it composes
// with whatever router or middleware chain the application already has —
// the same reason rest.Resource takes a huma.API rather than owning one.
//
// A missing or rejected credential answers 401 and never calls next.
func Middleware[T any](v Verifier[T], extract CredentialExtractor) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cred, ok := extract(r)
			if !ok {
				writeProblem(w, http.StatusUnauthorized, "the request carries no credential")
				return
			}

			principal, err := v.Verify(r.Context(), cred)
			if err != nil {
				var transient TransientError
				if errors.As(err, &transient) {
					writeProblem(w, http.StatusInternalServerError, "authentication could not be completed")
					return
				}
				// err is deliberately not echoed: which check a forged or
				// expired credential failed is useful to precisely one kind
				// of caller.
				writeProblem(w, http.StatusUnauthorized, "the credential is not valid")
				return
			}

			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

// writeProblem writes the same RFC 9457 problem shape example/tasks/auth
// and the rest package both use, so a client sees one error type across
// authentication failures and everything rest itself rejects.
func writeProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"title":  http.StatusText(status),
		"status": status,
		"detail": detail,
	})
}
