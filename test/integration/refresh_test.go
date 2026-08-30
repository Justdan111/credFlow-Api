//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/Justdan111/credflow-api/internal/auth"
)

// newClient returns an http.Client with its own cookie jar, so each client
// behaves like a separate browser — which is what makes the per-device
// family semantics testable.
func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("new cookie jar: %v", err)
	}
	return &http.Client{Jar: jar}
}

// post sends a request through the given client so cookies are stored and
// replayed automatically.
func post(t *testing.T, c *http.Client, url, body string) (int, envelope) {
	t.Helper()

	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var env envelope
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &env)
	}
	return resp.StatusCode, env
}

func registerVia(t *testing.T, c *http.Client, baseURL, email string) string {
	t.Helper()
	body := `{"businessName":"Biz ` + email + `","email":"` + email +
		`","password":"longenoughpw","name":"Tester"}`

	status, env := post(t, c, baseURL+"/api/auth/register", body)
	if status != http.StatusCreated {
		t.Fatalf("register: status %d", status)
	}
	var data struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return data.AccessToken
}

// refreshCookie returns the current refresh cookie held by the client's jar.
func refreshCookie(t *testing.T, c *http.Client, baseURL string) *http.Cookie {
	t.Helper()
	u, err := url.Parse(baseURL + "/api/auth")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == auth.RefreshCookieName {
			return ck
		}
	}
	return nil
}

func TestRegisterSetsHttpOnlyRefreshCookie(t *testing.T) {
	baseURL, _ := newTestServer(t)
	c := newClient(t)

	access := registerVia(t, c, baseURL, "cookie@refresh.test")
	if access == "" {
		t.Fatal("no accessToken returned")
	}
	if refreshCookie(t, c, baseURL) == nil {
		t.Fatal("no refresh cookie set on register")
	}
}

func TestRefreshRotatesTheToken(t *testing.T) {
	baseURL, _ := newTestServer(t)
	c := newClient(t)

	firstAccess := registerVia(t, c, baseURL, "rotate@refresh.test")
	firstRefresh := refreshCookie(t, c, baseURL).Value

	status, env := post(t, c, baseURL+"/api/auth/refresh", "")
	if status != http.StatusOK {
		t.Fatalf("refresh: status %d", status)
	}

	var data struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if data.AccessToken == "" {
		t.Fatal("refresh returned no accessToken")
	}
	// The new access token may be byte-identical to the old one when both are
	// minted within the same second: JWT iat/exp have one-second granularity,
	// so identical claims produce identical tokens. That is correct, so assert
	// the token *works* rather than that it differs.
	_ = firstAccess
	if s, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/auth/me", data.AccessToken, nil); s != http.StatusOK {
		t.Errorf("access token from refresh does not authenticate: /me returned %d", s)
	}

	secondRefresh := refreshCookie(t, c, baseURL).Value
	if secondRefresh == firstRefresh {
		t.Fatal("refresh token was not rotated")
	}
}

// The headline test: replaying a retired token must revoke the whole family,
// so the attacker's stolen token AND the victim's live chain both stop working.
func TestRefreshReuseRevokesTheFamily(t *testing.T) {
	baseURL, _ := newTestServer(t)
	victim := newClient(t)

	registerVia(t, victim, baseURL, "reuse@refresh.test")
	stolen := refreshCookie(t, victim, baseURL).Value

	// Victim refreshes normally: `stolen` is now retired.
	if status, _ := post(t, victim, baseURL+"/api/auth/refresh", ""); status != http.StatusOK {
		t.Fatalf("legitimate refresh: status %d", status)
	}
	live := refreshCookie(t, victim, baseURL).Value

	// Attacker replays the retired token from a different client.
	attacker := newClient(t)
	setRefreshCookie(t, attacker, baseURL, stolen)

	if status, _ := post(t, attacker, baseURL+"/api/auth/refresh", ""); status != http.StatusUnauthorized {
		t.Fatalf("replay: status %d, want 401", status)
	}

	// The victim's still-live token must now be dead too — that is the point.
	victimAfter := newClient(t)
	setRefreshCookie(t, victimAfter, baseURL, live)

	if status, _ := post(t, victimAfter, baseURL+"/api/auth/refresh", ""); status != http.StatusUnauthorized {
		t.Fatalf("victim's live token still works after reuse detection: status %d, want 401", status)
	}
}

// Revoking one family must not touch another: logging out or being attacked on
// a laptop should leave the phone signed in.
func TestReuseDoesNotAffectOtherSessions(t *testing.T) {
	baseURL, _ := newTestServer(t)

	const email = "twodevices@refresh.test"
	laptop := newClient(t)
	registerVia(t, laptop, baseURL, email)
	laptopStolen := refreshCookie(t, laptop, baseURL).Value

	// Second login from a second "device" opens a second family.
	phone := newClient(t)
	status, _ := post(t, phone, baseURL+"/api/auth/login",
		`{"email":"`+email+`","password":"longenoughpw"}`)
	if status != http.StatusOK {
		t.Fatalf("login on second device: status %d", status)
	}
	if refreshCookie(t, phone, baseURL) == nil {
		t.Fatal("second device got no refresh cookie")
	}

	// Burn the laptop's family via rotation + replay.
	if s, _ := post(t, laptop, baseURL+"/api/auth/refresh", ""); s != http.StatusOK {
		t.Fatalf("laptop refresh: status %d", s)
	}
	attacker := newClient(t)
	setRefreshCookie(t, attacker, baseURL, laptopStolen)
	if s, _ := post(t, attacker, baseURL+"/api/auth/refresh", ""); s != http.StatusUnauthorized {
		t.Fatalf("replay: status %d, want 401", s)
	}

	// The phone's separate family must still refresh.
	if s, _ := post(t, phone, baseURL+"/api/auth/refresh", ""); s != http.StatusOK {
		t.Fatalf("phone refresh after laptop family revoked: status %d, want 200", s)
	}
}

func TestLogoutRevokesTheSession(t *testing.T) {
	baseURL, _ := newTestServer(t)
	c := newClient(t)

	registerVia(t, c, baseURL, "logout@refresh.test")
	token := refreshCookie(t, c, baseURL).Value

	if status, _ := post(t, c, baseURL+"/api/auth/logout", ""); status != http.StatusNoContent {
		t.Fatalf("logout: status %d, want 204", status)
	}

	// The revoked token must not refresh, even presented directly.
	after := newClient(t)
	setRefreshCookie(t, after, baseURL, token)
	if status, _ := post(t, after, baseURL+"/api/auth/refresh", ""); status != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: status %d, want 401", status)
	}
}

// Logout must be idempotent: a second click cannot produce an error.
func TestLogoutIsIdempotent(t *testing.T) {
	baseURL, _ := newTestServer(t)
	c := newClient(t)

	registerVia(t, c, baseURL, "idem@refresh.test")

	for i := 0; i < 2; i++ {
		if status, _ := post(t, c, baseURL+"/api/auth/logout", ""); status != http.StatusNoContent {
			t.Fatalf("logout attempt %d: status %d, want 204", i+1, status)
		}
	}
}

func TestRefreshWithoutCookieIsUnauthorized(t *testing.T) {
	baseURL, _ := newTestServer(t)

	if status, _ := post(t, newClient(t), baseURL+"/api/auth/refresh", ""); status != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", status)
	}
}

func TestRefreshWithForgedCookieIsUnauthorized(t *testing.T) {
	baseURL, _ := newTestServer(t)

	c := newClient(t)
	setRefreshCookie(t, c, baseURL, "totally-made-up-token-value")

	if status, _ := post(t, c, baseURL+"/api/auth/refresh", ""); status != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", status)
	}
}

// The Origin check is defence in depth behind SameSite=Strict.
func TestRefreshRejectsForeignOrigin(t *testing.T) {
	baseURL, _ := newTestServer(t)
	c := newClient(t)
	registerVia(t, c, baseURL, "origin@refresh.test")

	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/auth/refresh", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example.com")

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}

// setRefreshCookie plants a specific refresh token into a client's jar,
// simulating a token captured from somewhere else.
func setRefreshCookie(t *testing.T, c *http.Client, baseURL, value string) {
	t.Helper()
	u, err := url.Parse(baseURL + "/api/auth")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	c.Jar.SetCookies(u, []*http.Cookie{{
		Name:  auth.RefreshCookieName,
		Value: value,
		Path:  "/api/auth",
	}})
}
