package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSetRefreshCookieAttributes(t *testing.T) {
	rec := httptest.NewRecorder()
	CookieConfig{Secure: true}.Set(rec, "tok123", 30*24*time.Hour)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want exactly 1", len(cookies))
	}
	c := cookies[0]

	if c.Name != RefreshCookieName {
		t.Errorf("Name = %q, want %q", c.Name, RefreshCookieName)
	}
	if c.Value != "tok123" {
		t.Errorf("Value = %q, want %q", c.Value, "tok123")
	}
	if !c.HttpOnly {
		t.Error("HttpOnly must be set — JavaScript must never read the refresh token")
	}
	if !c.Secure {
		t.Error("Secure must be set when configured")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict — it is the primary CSRF defence", c.SameSite)
	}
	if c.Path != refreshCookiePath {
		t.Errorf("Path = %q, want %q — the cookie must not travel to non-auth endpoints", c.Path, refreshCookiePath)
	}
	if c.MaxAge != int((30 * 24 * time.Hour).Seconds()) {
		t.Errorf("MaxAge = %d, want %d", c.MaxAge, int((30 * 24 * time.Hour).Seconds()))
	}
}

// Secure must follow configuration, so local http development still works.
func TestSetRefreshCookieHonoursInsecureConfig(t *testing.T) {
	rec := httptest.NewRecorder()
	CookieConfig{Secure: false}.Set(rec, "tok", time.Hour)

	if rec.Result().Cookies()[0].Secure {
		t.Error("Secure set despite Secure:false config")
	}
}

func TestClearRefreshCookieDeletesIt(t *testing.T) {
	rec := httptest.NewRecorder()
	CookieConfig{Secure: true}.Clear(rec)

	c := rec.Result().Cookies()[0]
	if c.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want negative so the browser deletes it", c.MaxAge)
	}
	if c.Value != "" {
		t.Errorf("Value = %q, want empty", c.Value)
	}
	// Path must match the Set call or the browser keeps the original cookie.
	if c.Path != refreshCookiePath {
		t.Errorf("Path = %q, want %q — a mismatched path leaves the cookie in place", c.Path, refreshCookiePath)
	}
}

func TestReadRefreshCookie(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
		r.AddCookie(&http.Cookie{Name: RefreshCookieName, Value: "abc"})

		got, ok := ReadRefreshCookie(r)
		if !ok || got != "abc" {
			t.Fatalf("got (%q, %v), want (\"abc\", true)", got, ok)
		}
	})

	t.Run("absent", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
		if _, ok := ReadRefreshCookie(r); ok {
			t.Fatal("missing cookie reported as present")
		}
	})

	t.Run("empty value", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
		r.AddCookie(&http.Cookie{Name: RefreshCookieName, Value: ""})
		if _, ok := ReadRefreshCookie(r); ok {
			t.Fatal("empty cookie reported as present")
		}
	})
}
