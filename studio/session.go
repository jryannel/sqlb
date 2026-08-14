package studio

import "net/http"

// tokenCookie holds the operator's own bearer token, never a service
// credential — see docs/adr/0053's revision, "authenticates as the caller."
const tokenCookie = "sqlb_studio_token"

func tokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(tokenCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// cookiePath is the cookie's own Path attribute — where the browser will
// send it back — not to be confused with s.url, which builds a page's
// href/redirect target. basePath's normalized form has no trailing slash;
// the cookie attribute needs one, and "" must become "/" rather than "".
func cookiePath(basePath string) string {
	if basePath == "" {
		return "/"
	}
	return basePath + "/"
}

func (s *Server) setTokenCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     tokenCookie,
		Value:    token,
		Path:     cookiePath(s.basePath),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearTokenCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     tokenCookie,
		Value:    "",
		Path:     cookiePath(s.basePath),
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
