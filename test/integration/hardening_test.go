//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// assertEnvelope fails unless the body is the standard envelope carrying an
// error message. The whole point of one response shape is that a client never
// has to guess whether this particular failure is JSON.
func assertEnvelope(t *testing.T, raw []byte, context string) {
	t.Helper()
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("%s: response is not JSON: %v (body: %s)", context, err, raw)
	}
	if env.Error == nil || env.Error.Message == "" {
		t.Errorf("%s: envelope carries no error message: %s", context, raw)
	}
}

func TestHardening_unroutedAndWrongMethodUseTheEnvelope(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "harden1@acme.test")

	t.Run("unknown path", func(t *testing.T) {
		// chi's default is the plain string "404 page not found".
		status, _, raw := doJSON(t, http.MethodGet, baseURL+"/api/does-not-exist", token, nil)
		if status != http.StatusNotFound {
			t.Errorf("status: got %d, want 404", status)
		}
		assertEnvelope(t, raw, "404")
	})

	t.Run("wrong method on a known path", func(t *testing.T) {
		// chi's default here is an empty body, which tells a client nothing.
		status, _, raw := doJSON(t, http.MethodPut, baseURL+"/api/customers", token, map[string]string{"name": "x"})
		if status != http.StatusMethodNotAllowed {
			t.Errorf("status: got %d, want 405", status)
		}
		assertEnvelope(t, raw, "405")
	})
}

func TestHardening_malformedUUIDIsAClientError(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "harden2@acme.test")

	// Before validation these reached Postgres, which refused the cast, and the
	// repository error surfaced as a 500 — a client mistake reported as an
	// outage, and noise in the error logs that looks like one.
	paths := []string{
		"/api/customers/not-a-uuid",
		"/api/customers/12345/debts",
		"/api/debts/abc",
		"/api/debts/abc/payments",
		"/api/payments/xyz",
		"/api/users/nope",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			status, _, raw := doJSON(t, http.MethodGet, baseURL+path, token, nil)
			if status != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400 (%s)", status, raw)
			}
			assertEnvelope(t, raw, path)
		})
	}

	t.Run("a well-formed but unknown id still 404s", func(t *testing.T) {
		// The check must not swallow the difference between "malformed" and
		// "no such row".
		status, _, _ := doJSON(t, http.MethodGet,
			baseURL+"/api/customers/00000000-0000-0000-0000-000000000000", token, nil)
		if status != http.StatusNotFound {
			t.Errorf("status: got %d, want 404", status)
		}
	})
}

func TestHardening_writesMustDeclareJSON(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "harden3@acme.test")

	// doJSON always sets application/json, so this one is built by hand.
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/customers",
		strings.NewReader(`{"name":"Form Post"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// text/plain is what an HTML form can send and JSON fetch cannot, which is
	// the shape that matters if cookie auth is ever added for convenience.
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("status: got %d, want 415", resp.StatusCode)
	}
}

// TestHardening_authenticatedWritesAreThrottled covers the gap phase 12 left:
// once a caller held a token, nothing capped them at all.
func TestHardening_authenticatedWritesAreThrottled(t *testing.T) {
	baseURL, _ := newTestServer(t)
	owner := registerAndLogin(t, baseURL, "harden4@acme.test")

	var limited bool
	var retryAfter string
	for i := 0; i < testAuthedWriteLimit+3; i++ {
		req, err := http.NewRequest(http.MethodPost, baseURL+"/api/customers",
			strings.NewReader(fmt.Sprintf(`{"name":"Flood %d"}`, i)))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+owner)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			retryAfter = resp.Header.Get("Retry-After")
			break
		}
	}

	if !limited {
		t.Fatalf("no 429 after %d writes — authenticated traffic is uncapped", testAuthedWriteLimit+3)
	}
	// Without Retry-After a client can only guess, and guessing means retrying
	// immediately and staying limited.
	seconds, err := strconv.Atoi(retryAfter)
	if err != nil || seconds < 1 {
		t.Errorf("Retry-After: got %q, want a positive number of seconds", retryAfter)
	}
}

// TestHardening_writeLimitIsPerUserNotPerAddress is the counterpart the Phase 12
// reasoning demands: on carrier-grade NAT, colleagues share one public address,
// so an IP-keyed limit would let one busy person lock out the whole office.
func TestHardening_writeLimitIsPerUserNotPerAddress(t *testing.T) {
	baseURL, _ := newTestServer(t)
	first := registerAndLogin(t, baseURL, "harden5a@acme.test")
	second := registerAndLogin(t, baseURL, "harden5b@acme.test")

	// Exhaust the first user's allowance.
	for i := 0; i < testAuthedWriteLimit+2; i++ {
		doJSON(t, http.MethodPost, baseURL+"/api/customers", first,
			map[string]string{"name": fmt.Sprintf("First %d", i)})
	}
	status, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/customers", first,
		map[string]string{"name": "First again"})
	if status != http.StatusTooManyRequests {
		t.Fatalf("first user should be limited by now, got %d", status)
	}

	// Both requests come from 127.0.0.1. The second user must be unaffected.
	status, _, raw := doJSON(t, http.MethodPost, baseURL+"/api/customers", second,
		map[string]string{"name": "Second user"})
	if status != http.StatusCreated {
		t.Errorf("second user blocked by the first user's limit: got %d (%s)", status, raw)
	}
}

func TestHardening_readsDoNotConsumeTheWriteAllowance(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "harden6@acme.test")

	// Listing is cheap and frequent; a dashboard polling it must never exhaust
	// the allowance that protects writes.
	for i := 0; i < testAuthedWriteLimit*3; i++ {
		if status, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/customers", token, nil); status != http.StatusOK {
			t.Fatalf("read %d was rejected with %d", i, status)
		}
	}

	status, _, raw := doJSON(t, http.MethodPost, baseURL+"/api/customers", token,
		map[string]string{"name": "Still allowed"})
	if status != http.StatusCreated {
		t.Errorf("reads consumed the write allowance: got %d (%s)", status, raw)
	}
}
