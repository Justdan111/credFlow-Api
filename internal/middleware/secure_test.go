package middleware

import (
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersAlwaysSet(t *testing.T) {
	rec := httptest.NewRecorder()
	SecurityHeaders(true)(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/customers", nil))

	want := map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "strict-origin-when-cross-origin",
		"Cross-Origin-Opener-Policy": "same-origin",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// HSTS on plain http would pin localhost to https in the developer's browser,
// which is tedious to undo and breaks local work.
func TestHSTSOnlyOverTLS(t *testing.T) {
	t.Run("absent on plain http", func(t *testing.T) {
		rec := httptest.NewRecorder()
		SecurityHeaders(true)(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
			t.Errorf("HSTS = %q on a plain http request, want empty", got)
		}
	})

	t.Run("present over TLS", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.TLS = &tls.ConnectionState{}
		rec := httptest.NewRecorder()
		SecurityHeaders(true)(okHandler()).ServeHTTP(rec, r)

		if rec.Header().Get("Strict-Transport-Security") == "" {
			t.Error("HSTS missing on a TLS request")
		}
	})

	t.Run("present behind a TLS-terminating proxy", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Forwarded-Proto", "https")
		rec := httptest.NewRecorder()
		SecurityHeaders(true)(okHandler()).ServeHTTP(rec, r)

		if rec.Header().Get("Strict-Transport-Security") == "" {
			t.Error("HSTS missing behind a TLS-terminating proxy")
		}
	})

	t.Run("absent when disabled", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.TLS = &tls.ConnectionState{}
		rec := httptest.NewRecorder()
		SecurityHeaders(false)(okHandler()).ServeHTTP(rec, r)

		if rec.Header().Get("Strict-Transport-Security") != "" {
			t.Error("HSTS set despite being disabled")
		}
	})
}

func TestBodyLimitCapsWrites(t *testing.T) {
	const cap = 100

	// Reads the body to completion, the way json.Decode does: MaxBytesReader
	// serves the first `cap` bytes happily and only errors past them, so a
	// single short Read would not reveal the cap.
	handler := BodyLimit(cap)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("within the cap", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 50)))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Errorf("status %d, want 200", rec.Code)
		}
	})

	t.Run("over the cap", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", cap+500)))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status %d, want 413", rec.Code)
		}
	})

	// GET carries no body worth wrapping.
	t.Run("GET is untouched", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		BodyLimit(cap)(okHandler()).ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Errorf("status %d, want 200", rec.Code)
		}
	})
}
