package sqlb

import (
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
