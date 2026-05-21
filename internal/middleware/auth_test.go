package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Justdan111/credflow-api/internal/auth"
)

// newTestStack returns a RequireAuth-wrapped handler that flags whether the
// inner ("next") handler ever ran and captures the identity it saw.
type captured struct {
	ran        bool
	userID     string
	businessID string
	role       string
}

func newTestStack(jwtSvc *auth.JWTService) (http.Handler, *captured) {
	cap := &captured{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.ran = true
		cap.userID, _ = auth.UserIDFromContext(r.Context())
		cap.businessID, _ = auth.BusinessIDFromContext(r.Context())
		cap.role, _ = auth.RoleFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	return RequireAuth(jwtSvc)(next), cap
}

func TestRequireAuth_validTokenInjectsContext(t *testing.T) {
	jwtSvc := auth.NewJWTService("test-secret", time.Hour)
	token, err := jwtSvc.Mint("user-123", "biz-456", "owner")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	handler, cap := newTestStack(jwtSvc)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	if !cap.ran {
		t.Fatal("next handler did not run")
	}
	if cap.userID != "user-123" {
		t.Errorf("userID in context: got %q, want user-123", cap.userID)
	}
	if cap.businessID != "biz-456" {
		t.Errorf("businessID in context: got %q, want biz-456", cap.businessID)
	}
	if cap.role != "owner" {
		t.Errorf("role in context: got %q, want owner", cap.role)
	}
}

func TestRequireAuth_rejectsMissingAndMalformed(t *testing.T) {
	jwtSvc := auth.NewJWTService("test-secret", time.Hour)
	validToken, _ := jwtSvc.Mint("u", "b", "owner")

	tests := []struct {
		name       string
		authHeader string
	}{
		{"no header at all", ""},
		{"empty header value", " "},
		{"missing Bearer prefix", validToken},
		{"wrong scheme", "Basic " + validToken},
		{"Bearer with empty token", "Bearer "},
		{"Bearer with garbage token", "Bearer not.a.real.token"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, cap := newTestStack(jwtSvc)
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status: got %d, want 401", rec.Code)
			}
			if cap.ran {
				t.Fatal("next handler ran on bad auth — middleware failed open")
			}
		})
	}
}

func TestRequireAuth_rejectsExpiredToken(t *testing.T) {
	expired := auth.NewJWTService("test-secret", -1*time.Second)
	verifier := auth.NewJWTService("test-secret", time.Hour) // same secret

	token, err := expired.Mint("u", "b", "owner")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	handler, cap := newTestStack(verifier)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rec.Code)
	}
	if cap.ran {
		t.Fatal("next handler ran on expired token")
	}
}
