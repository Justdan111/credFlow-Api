package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Justdan111/credflow-api/pkg/ratelimit"
)

func postJSON(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.10:54321"
	return r
}

func TestRateLimitBlocksPastTheLimit(t *testing.T) {
	h := RateLimit(ratelimit.NewMemory(), RateLimitRule{
		Name: "t", Limit: 2, Window: time.Minute, KeyFunc: ByIP,
	})(okHandler())

	for i := 1; i <= 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postJSON(`{}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(`{}`))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After missing — a client cannot tell when to retry")
	}
	// The message must not hint at whether an account exists.
	if body := rec.Body.String(); strings.Contains(strings.ToLower(body), "account") ||
		strings.Contains(strings.ToLower(body), "email") {
		t.Errorf("429 body leaks account information: %s", body)
	}
}

// The whole point of the composite key: carrier-grade NAT means unrelated users
// share an address, and one person's failed logins must not lock out another.
func TestRateLimitByIPAndEmailSeparatesUsersBehindOneNAT(t *testing.T) {
	h := RateLimit(ratelimit.NewMemory(), RateLimitRule{
		Name: "login", Limit: 2, Window: time.Minute, KeyFunc: ByIPAndEmail,
	})(okHandler())

	// Exhaust the first user's allowance from a shared address.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postJSON(`{"email":"first@shared.test"}`))
		_ = rec
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(`{"email":"first@shared.test"}`))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("first user status %d, want 429", rec.Code)
	}

	// A different user on the SAME address must be unaffected.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(`{"email":"second@shared.test"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("second user behind the same IP got %d, want 200 — NAT collateral damage", rec.Code)
	}
}

// Case and whitespace must not multiply a caller's allowance.
func TestRateLimitNormalisesEmailKeys(t *testing.T) {
	h := RateLimit(ratelimit.NewMemory(), RateLimitRule{
		Name: "forgot", Limit: 1, Window: time.Minute, KeyFunc: ByEmailField,
	})(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(`{"email":"user@x.test"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: %d", rec.Code)
	}

	// Same address, different casing and padding.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(`{"email":"  USER@X.test  "}`))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429 — case and whitespace must share one bucket", rec.Code)
	}
}

// The middleware reads the body to find the email; the handler must still get it.
func TestRateLimitRestoresTheBodyForTheHandler(t *testing.T) {
	var seen string
	h := RateLimit(ratelimit.NewMemory(), RateLimitRule{
		Name: "t", Limit: 5, Window: time.Minute, KeyFunc: ByEmailField,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = string(b)
		w.WriteHeader(http.StatusOK)
	}))

	const body = `{"email":"a@b.test","password":"secret"}`
	h.ServeHTTP(httptest.NewRecorder(), postJSON(body))

	if seen != body {
		t.Fatalf("handler saw %q, want the original body %q", seen, body)
	}
}

// A rule whose key cannot be derived must stand down, not block the request.
func TestRateLimitSkipsRuleWithNoKey(t *testing.T) {
	h := RateLimit(ratelimit.NewMemory(), RateLimitRule{
		Name: "t", Limit: 1, Window: time.Minute, KeyFunc: ByEmailField,
	})(okHandler())

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postJSON(`{"no":"email here"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d blocked despite no derivable key: %d", i, rec.Code)
		}
	}
}

// THE bypass this phase exists to prevent: without chi's RealIP mounted,
// RemoteAddr is the true TCP peer, so forging X-Forwarded-For changes nothing.
func TestRateLimitIgnoresForgedForwardedForByDefault(t *testing.T) {
	h := RateLimit(ratelimit.NewMemory(), RateLimitRule{
		Name: "t", Limit: 2, Window: time.Minute, KeyFunc: ByIP,
	})(okHandler())

	for i := 0; i < 2; i++ {
		h.ServeHTTP(httptest.NewRecorder(), postJSON(`{}`))
	}

	// A different spoofed header on every request would reset the bucket if
	// the header were trusted.
	for i, spoof := range []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"} {
		r := postJSON(`{}`)
		r.Header.Set("X-Forwarded-For", spoof)
		r.Header.Set("X-Real-IP", spoof)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("spoof %d (%s) got %d, want 429 — the limit was bypassed by a forged header",
				i, spoof, rec.Code)
		}
	}
}

func TestClientIPStripsThePort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.7:44321"
	if got := ClientIP(r); got != "198.51.100.7" {
		t.Errorf("ClientIP = %q, want 198.51.100.7", got)
	}

	// An address with no port must pass through rather than blow up.
	r.RemoteAddr = "198.51.100.7"
	if got := ClientIP(r); got != "198.51.100.7" {
		t.Errorf("ClientIP = %q for a port-less address", got)
	}
}

// Multiple rules on one route must all be enforced.
func TestRateLimitAppliesEveryRule(t *testing.T) {
	h := RateLimit(ratelimit.NewMemory(),
		RateLimitRule{Name: "email", Limit: 5, Window: time.Minute, KeyFunc: ByEmailField},
		RateLimitRule{Name: "ip", Limit: 2, Window: time.Minute, KeyFunc: ByIP},
	)(okHandler())

	// Different addresses each stay under the per-email limit, but together
	// they must trip the tighter per-IP ceiling.
	for i, email := range []string{"a@x.test", "b@x.test"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postJSON(`{"email":"`+email+`"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d (%s): %d", i, email, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(`{"email":"c@x.test"}`))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429 — the per-IP ceiling should have tripped", rec.Code)
	}
}
