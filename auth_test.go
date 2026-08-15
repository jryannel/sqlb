package sqlb_test

import (
	"context"
	"errors"
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

func TestTransientError(t *testing.T) {
	inner := errors.New("dial tcp: connection refused")
	te := sqlb.TransientError{Err: inner}

	if got := te.Error(); got != inner.Error() {
		t.Fatalf("Error() = %q, want %q", got, inner.Error())
	}
	if !errors.Is(te, inner) {
		t.Fatalf("errors.Is(te, inner) = false, want true (Unwrap must expose Err)")
	}

	var target sqlb.TransientError
	if !errors.As(error(te), &target) {
		t.Fatalf("errors.As did not recognize TransientError")
	}
}

// verifierFunc lets a test supply Verify as a closure instead of a named type.
type verifierFunc[T any] func(ctx context.Context, cred string) (T, error)

func (f verifierFunc[T]) Verify(ctx context.Context, cred string) (T, error) {
	return f(ctx, cred)
}

func TestVerifierInterface(t *testing.T) {
	// Compile-time check: verifierFunc[string] must satisfy Verifier[string].
	var _ sqlb.Verifier[string] = verifierFunc[string](func(ctx context.Context, cred string) (string, error) {
		return cred, nil
	})
}

type testPrincipal struct{ ID string }

func TestMiddleware_Success(t *testing.T) {
	v := verifierFunc[testPrincipal](func(ctx context.Context, cred string) (testPrincipal, error) {
		if cred != "good-token" {
			t.Fatalf("Verify received %q, want %q", cred, "good-token")
		}
		return testPrincipal{ID: "user-1"}, nil
	})

	var gotPrincipal testPrincipal
	var gotOK bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrincipal, gotOK = sqlb.PrincipalFrom[testPrincipal](r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mw := sqlb.Middleware[testPrincipal](v, sqlb.BearerToken)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer good-token")
	w := httptest.NewRecorder()

	mw(next).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if !gotOK {
		t.Fatalf("PrincipalFrom[testPrincipal] found nothing in next's context")
	}
	if gotPrincipal.ID != "user-1" {
		t.Fatalf("principal.ID = %q, want %q", gotPrincipal.ID, "user-1")
	}
}
