package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Middleware verifies the bearer token on every request and puts the claims in
// the context, where hooks can reach them.
//
// It is ordinary net/http middleware rather than anything sqlb- or Huma-
// specific, so it composes with whatever else the router already runs. That is
// the same reason rest.Resource takes a huma.API rather than a router: the
// application owns its middleware stack.
//
// # Why an allow-list rather than opt-in protection
//
// Requests are rejected unless their path is explicitly public. The other way
// round — protect the routes you remember to protect — fails silently, and it
// fails in the direction where the mistake is invisible in testing and visible
// in an incident. Here, forgetting to list a new public route breaks it loudly
// during development; forgetting to protect a new private route does nothing,
// because it was already protected.
//
// The hooks fail closed as well, so an unauthenticated request that somehow
// reached a handler still cannot read another workspace's rows. Two independent
// checks, because the interesting failures are the ones where the first is
// bypassed.
func Middleware(s *Signer, public ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublic(r.URL.Path, public) {
				next.ServeHTTP(w, r)
				return
			}

			token, ok := bearer(r.Header.Get("Authorization"))
			if !ok {
				// RFC 6750: a 401 says which scheme the client should use.
				// Without it a browser or generated client has to guess.
				w.Header().Set("WWW-Authenticate", `Bearer realm="tasks"`)
				unauthorized(w, "the request carries no bearer token")
				return
			}

			claims, err := s.Verify(token)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="tasks", error="invalid_token"`)
				// err is deliberately not echoed. Which check a forgery failed
				// is useful to precisely one kind of caller.
				unauthorized(w, "the bearer token is not valid")
				return
			}

			next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), claims)))
		})
	}
}

// isPublic matches a path against the public list. An entry ending in "/"
// matches by prefix — "/docs/" covers the assets a docs page pulls in — and
// anything else must match exactly, so "/tasks" cannot be opened up by an entry
// meant for "/task".
func isPublic(path string, public []string) bool {
	for _, p := range public {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(path, p) {
				return true
			}
			continue
		}
		if path == p {
			return true
		}
	}
	return false
}

// bearer extracts the token from an Authorization header. The scheme is matched
// case-insensitively because RFC 7235 says it is case-insensitive, and clients
// send "bearer", "Bearer" and occasionally "BEARER".
func bearer(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

// unauthorized writes the same RFC 9457 problem shape the rest package uses, so
// a client sees one error type across the whole API rather than one for
// authentication and another for everything else.
func unauthorized(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"title":  http.StatusText(http.StatusUnauthorized),
		"status": http.StatusUnauthorized,
		"detail": detail,
	})
}
