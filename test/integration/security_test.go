//go:build integration

package integration

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestSecurity_headersOnEveryResponse(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "h@sec.test")

	check := func(t *testing.T, path, tok string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, baseURL+path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer resp.Body.Close()

		want := map[string]string{
			"X-Content-Type-Options":     "nosniff",
			"X-Frame-Options":            "DENY",
			"Referrer-Policy":            "strict-origin-when-cross-origin",
			"Cross-Origin-Opener-Policy": "same-origin",
		}
		for k, v := range want {
			if got := resp.Header.Get(k); got != v {
				t.Errorf("%s on %s = %q, want %q", k, path, got, v)
			}
		}
	}

	t.Run("success response", func(t *testing.T) { check(t, "/api/customers", token) })
	// Headers must be present on failures too, not only happy paths.
	t.Run("error response", func(t *testing.T) { check(t, "/api/customers", "") })
	t.Run("health", func(t *testing.T) { check(t, "/health", "") })

	// HSTS must stay off over plain http, or a developer's browser pins
	// localhost to https.
	t.Run("no HSTS over plain http", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/health")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
			t.Errorf("HSTS = %q over plain http, want empty", got)
		}
	})
}

func TestSecurity_oversizedBodyRejected(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "b@sec.test")

	// Comfortably past testMaxBodyBytes.
	huge := strings.Repeat("x", testMaxBodyBytes*3)

	st, _, raw := doJSON(t, http.MethodPost, baseURL+"/api/customers", token,
		map[string]any{"name": "C", "email": "big@sec.test", "notes": huge})

	if st != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413 (body was %d bytes, cap %d): %s",
			st, len(huge), testMaxBodyBytes, raw)
	}

	// A normal body must still work.
	if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/customers", token,
		map[string]any{"name": "Normal", "email": "normal@sec.test"}); st != http.StatusCreated {
		t.Errorf("a normal request returned %d, want 201", st)
	}
}

func TestSecurity_loginRateLimit(t *testing.T) {
	baseURL, _ := newTestServer(t)
	const email = "rl@sec.test"
	registerAndLogin(t, baseURL, email)

	attempt := func(e, pw string) (int, string) {
		t.Helper()
		body := strings.NewReader(`{"email":"` + e + `","password":"` + pw + `"}`)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/auth/login", body)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Retry-After")
	}

	// Burn the allowance with wrong passwords.
	for i := 0; i < testLoginLimit; i++ {
		if st, _ := attempt(email, "wrong-password"); st != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i+1, st)
		}
	}

	t.Run("the next attempt is throttled", func(t *testing.T) {
		st, retry := attempt(email, "wrong-password")
		if st != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429", st)
		}
		if retry == "" {
			t.Fatal("Retry-After missing")
		}
		if n, err := strconv.Atoi(retry); err != nil || n < 1 {
			t.Errorf("Retry-After = %q, want a positive number of seconds", retry)
		}
	})

	// Even the correct password is throttled — otherwise the limit would be a
	// free oracle for confirming a guess.
	t.Run("the correct password is throttled too", func(t *testing.T) {
		if st, _ := attempt(email, "longenoughpw"); st != http.StatusTooManyRequests {
			t.Errorf("status %d, want 429", st)
		}
	})

	// THE property that matters on carrier-grade NAT: a different account from
	// the same address is unaffected.
	t.Run("another user behind the same IP is unaffected", func(t *testing.T) {
		const other = "other@sec.test"
		registerAndLogin(t, baseURL, other)

		if st, _ := attempt(other, "longenoughpw"); st != http.StatusOK {
			t.Fatalf("status %d, want 200 — one user's failures must not lock out another behind the same NAT", st)
		}
	})
}

// Forging X-Forwarded-For must not reset a limit, or the whole scheme is
// bypassable by anyone who reads the docs.
func TestSecurity_forgedForwardedForDoesNotBypassTheLimit(t *testing.T) {
	baseURL, _ := newTestServer(t)
	const email = "spoof@sec.test"
	registerAndLogin(t, baseURL, email)

	attempt := func(spoof string) int {
		t.Helper()
		body := strings.NewReader(`{"email":"` + email + `","password":"wrong-password"}`)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/auth/login", body)
		req.Header.Set("Content-Type", "application/json")
		if spoof != "" {
			req.Header.Set("X-Forwarded-For", spoof)
			req.Header.Set("X-Real-IP", spoof)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	for i := 0; i < testLoginLimit; i++ {
		attempt("")
	}
	if st := attempt(""); st != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429 before the spoofing check", st)
	}

	for _, spoof := range []string{"1.2.3.4", "5.6.7.8", "203.0.113.99"} {
		if st := attempt(spoof); st != http.StatusTooManyRequests {
			t.Fatalf("X-Forwarded-For: %s returned %d, want 429 — the limit was bypassed", spoof, st)
		}
	}
}

// forgot-password is the email-bombing vector: unauthenticated, and it sends
// mail to an address the caller chooses.
func TestSecurity_forgotPasswordRateLimit(t *testing.T) {
	baseURL, _ := newTestServer(t)
	const email = "fp@sec.test"
	registerAndLogin(t, baseURL, email)

	before := testMailer.count()

	for i := 0; i < testForgotLimit; i++ {
		if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/forgot-password", "",
			map[string]string{"email": email}); st != http.StatusAccepted {
			t.Fatalf("request %d: status %d, want 202", i+1, st)
		}
	}

	st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/forgot-password", "",
		map[string]string{"email": email})
	if st != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", st)
	}

	// The throttled request must not have sent mail.
	if got := testMailer.count() - before; got != testForgotLimit {
		t.Errorf("sent %d mails, want %d — a throttled request still sent one", got, testForgotLimit)
	}

	// A different address has its own bucket, so one person cannot block
	// everybody else's recovery.
	t.Run("a different address is unaffected", func(t *testing.T) {
		const other = "fp-other@sec.test"
		registerAndLogin(t, baseURL, other)

		if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/forgot-password", "",
			map[string]string{"email": other}); st != http.StatusAccepted {
			t.Errorf("status %d, want 202", st)
		}
	})
}

// The 429 must not become the enumeration oracle forgot-password avoids being.
func TestSecurity_throttleDoesNotLeakAccountExistence(t *testing.T) {
	baseURL, _ := newTestServer(t)

	for i := 0; i < testForgotLimit+1; i++ {
		doJSON(t, http.MethodPost, baseURL+"/api/auth/forgot-password", "",
			map[string]string{"email": "ghost@sec.test"})
	}

	st, _, raw := doJSON(t, http.MethodPost, baseURL+"/api/auth/forgot-password", "",
		map[string]string{"email": "ghost@sec.test"})
	if st != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", st)
	}

	body := strings.ToLower(string(raw))
	for _, leak := range []string{"account", "registered", "exists", "unknown", "ghost@sec.test"} {
		if strings.Contains(body, leak) {
			t.Errorf("the 429 body mentions %q, which leaks account state: %s", leak, raw)
		}
	}
}
