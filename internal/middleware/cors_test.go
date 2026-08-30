package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

var allowed = []string{"https://app.example.com", "http://localhost:5173"}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/customers", nil)
	r.Header.Set("Origin", "https://evil.example.com")

	CORS(allowed)(okHandler()).ServeHTTP(rec, r)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("echoed a non-allowlisted origin: %q", got)
	}
}

func TestCORSAllowsListedOriginWithCredentials(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/customers", nil)
	r.Header.Set("Origin", "https://app.example.com")

	CORS(allowed)(okHandler()).ServeHTTP(rec, r)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin = %q, want the exact origin echoed back", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q, want \"true\" — the refresh cookie needs it", got)
	}
	// Without Vary, a cache could serve one origin's response to another.
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want \"Origin\"", got)
	}
}

// A wildcard is invalid with credentials; browsers reject it outright.
func TestCORSNeverUsesWildcard(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/customers", nil)
	r.Header.Set("Origin", "https://app.example.com")

	CORS(allowed)(okHandler()).ServeHTTP(rec, r)

	if rec.Header().Get("Access-Control-Allow-Origin") == "*" {
		t.Fatal("wildcard origin is invalid alongside Allow-Credentials")
	}
}

func TestCORSHandlesPreflight(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/api/auth/refresh", nil)
	r.Header.Set("Origin", "https://app.example.com")
	r.Header.Set("Access-Control-Request-Method", "POST")

	CORS(allowed)(okHandler()).ServeHTTP(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("preflight must advertise allowed methods")
	}
}

// Non-browser clients send no Origin at all and must not be blocked.
func TestCORSPassesThroughRequestWithoutOrigin(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/customers", nil)

	CORS(allowed)(okHandler()).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestRequireAllowedOriginBlocksForeignOrigin(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	r.Header.Set("Origin", "https://evil.example.com")

	RequireAllowedOrigin(allowed)(okHandler()).ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d for a foreign Origin", rec.Code, http.StatusForbidden)
	}
}

func TestRequireAllowedOriginAllowsListedAndAbsent(t *testing.T) {
	t.Run("listed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
		r.Header.Set("Origin", "http://localhost:5173")

		RequireAllowedOrigin(allowed)(okHandler()).ServeHTTP(rec, r)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	// Absent Origin means a non-browser client; SameSite=Strict already
	// covers the browser CSRF case, so this must not be blocked.
	t.Run("absent", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)

		RequireAllowedOrigin(allowed)(okHandler()).ServeHTTP(rec, r)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}
