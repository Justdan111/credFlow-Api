package auth

import (
	"net/http"
	"time"
)

const (
	// RefreshCookieName is exported so integration tests and the frontend
	// contract refer to one definition.
	RefreshCookieName = "refresh_token"

	// refreshCookiePath scopes the cookie to the auth routes. The browser then
	// never transmits it on the other endpoints, so it cannot leak anywhere it
	// is not actually needed. Clear must use the same path or the browser
	// treats it as a different cookie and keeps the original.
	refreshCookiePath = "/api/auth"
)

// CookieConfig carries the environment-dependent attribute. Secure must be
// false for local http development and true everywhere else, so it comes from
// configuration rather than being hard-coded.
type CookieConfig struct {
	Secure bool
}

// Set writes the refresh cookie.
//
// HttpOnly is the point of the whole design: JavaScript cannot read the value,
// so an XSS bug cannot exfiltrate a long-lived credential. SameSite=Strict is
// the primary CSRF defence — the browser will not attach this cookie to a
// request originating from another site.
func (c CookieConfig) Set(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    token,
		Path:     refreshCookiePath,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   c.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// Clear deletes the refresh cookie. A negative MaxAge is the browser's delete
// signal; every other attribute must match Set or the deletion is ignored.
func (c CookieConfig) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// ReadRefreshCookie returns the token carried by the request, if any. An empty
// value counts as absent — a cleared cookie can still arrive as an empty string.
func ReadRefreshCookie(r *http.Request) (string, bool) {
	c, err := r.Cookie(RefreshCookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}
