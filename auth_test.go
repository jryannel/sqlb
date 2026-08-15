package sqlb_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jryannel/sqlb"
)

func TestBearerToken(t *testing.T) {
	cases := []struct {
		name      string
		header    string
		wantCred  string
		wantOK    bool
	}{
		{"present", "Bearer abc123", "abc123", true},
		{"case-insensitive scheme", "bearer abc123", "abc123", true},
		{"upper scheme", "BEARER abc123", "abc123", true},
		{"missing header", "", "", false},
		{"wrong scheme", "Basic abc123", "", false},
		{"empty token", "Bearer ", "", false},
		{"whitespace-only token", "Bearer    ", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}

			cred, ok := sqlb.BearerToken(r)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if cred != tc.wantCred {
				t.Fatalf("cred = %q, want %q", cred, tc.wantCred)
			}
		})
	}
}
