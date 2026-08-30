//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// loginWithJar signs in through a cookie jar so the refresh cookie is kept,
// which is what identifies a session.
func loginWithJar(t *testing.T, c *http.Client, baseURL, email string) string {
	t.Helper()
	status, env := post(t, c, baseURL+"/api/auth/login",
		`{"email":"`+email+`","password":"longenoughpw"}`)
	if status != http.StatusOK {
		t.Fatalf("login: status %d", status)
	}
	var d struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return d.AccessToken
}

func TestProfile_getAndUpdate(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "p@profile.test")

	t.Run("update name and phone", func(t *testing.T) {
		st, env, raw := doJSON(t, http.MethodPatch, baseURL+"/api/auth/me", token,
			map[string]any{"name": "Amina Bello", "phone": "+2348012345678"})
		if st != http.StatusOK {
			t.Fatalf("status %d, %s", st, raw)
		}
		var p struct {
			Name  string `json:"name"`
			Phone string `json:"phone"`
			Email string `json:"email"`
		}
		decode(t, env.Data, &p)

		if p.Name != "Amina Bello" || p.Phone != "+2348012345678" {
			t.Fatalf("got %+v", p)
		}
	})

	t.Run("partial update leaves other fields alone", func(t *testing.T) {
		_, env, _ := doJSON(t, http.MethodPatch, baseURL+"/api/auth/me", token,
			map[string]any{"name": "Amina B."})
		var p struct {
			Name  string `json:"name"`
			Phone string `json:"phone"`
		}
		decode(t, env.Data, &p)

		if p.Phone == "" {
			t.Error("phone was cleared by an update that did not mention it")
		}
	})

	// Changing a login identifier needs its own verification flow; silently
	// allowing it would let a stolen token lock the real owner out.
	t.Run("email cannot be changed here", func(t *testing.T) {
		_, env, _ := doJSON(t, http.MethodPatch, baseURL+"/api/auth/me", token,
			map[string]any{"email": "attacker@evil.test"})
		var p struct {
			Email string `json:"email"`
		}
		decode(t, env.Data, &p)

		if p.Email != "p@profile.test" {
			t.Fatalf("email changed to %q via PATCH /me", p.Email)
		}
	})

	t.Run("blank name rejected", func(t *testing.T) {
		if st, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/auth/me", token,
			map[string]any{"name": "   "}); st != http.StatusBadRequest {
			t.Errorf("status %d, want 400", st)
		}
	})
}

func TestSessions_listAndRevoke(t *testing.T) {
	baseURL, _ := newTestServer(t)
	const email = "s@session.test"
	registerAndLogin(t, baseURL, email)

	// Two independent cookie jars behave like two devices.
	laptop := newClient(t)
	phone := newClient(t)
	laptopToken := loginWithJar(t, laptop, baseURL, email)
	loginWithJar(t, phone, baseURL, email)

	listVia := func(c *http.Client, token string) []struct {
		ID        string `json:"id"`
		UserAgent string `json:"userAgent"`
		Current   bool   `json:"current"`
	} {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/auth/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)

		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decode: %v (%s)", err, raw)
		}
		var out []struct {
			ID        string `json:"id"`
			UserAgent string `json:"userAgent"`
			Current   bool   `json:"current"`
		}
		decode(t, env.Data, &out)
		return out
	}

	t.Run("both logins appear", func(t *testing.T) {
		sessions := listVia(laptop, laptopToken)
		if len(sessions) != 3 {
			// register + two logins = three families.
			t.Fatalf("got %d sessions, want 3 (register plus two logins)", len(sessions))
		}
	})

	t.Run("the calling session is flagged current", func(t *testing.T) {
		sessions := listVia(laptop, laptopToken)
		current := 0
		for _, s := range sessions {
			if s.Current {
				current++
			}
		}
		if current != 1 {
			t.Fatalf("got %d sessions flagged current, want exactly 1", current)
		}
	})

	t.Run("revoking a session kills only that one", func(t *testing.T) {
		sessions := listVia(laptop, laptopToken)
		var victim string
		for _, s := range sessions {
			if !s.Current {
				victim = s.ID
				break
			}
		}
		if victim == "" {
			t.Fatal("no non-current session to revoke")
		}

		req, _ := http.NewRequest(http.MethodDelete, baseURL+"/api/auth/sessions/"+victim, nil)
		req.Header.Set("Authorization", "Bearer "+laptopToken)
		resp, err := laptop.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("revoke: status %d, want 204", resp.StatusCode)
		}

		after := listVia(laptop, laptopToken)
		if len(after) != 2 {
			t.Fatalf("got %d sessions after revoking one, want 2", len(after))
		}
		for _, s := range after {
			if s.ID == victim {
				t.Fatal("the revoked session is still listed")
			}
		}
	})

	// One user must not be able to end another's session.
	t.Run("cannot revoke another user's session", func(t *testing.T) {
		otherToken := registerAndLogin(t, baseURL, "other@session.test")
		sessions := listVia(laptop, laptopToken)

		st, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/auth/sessions/"+sessions[0].ID, otherToken, nil)
		if st != http.StatusNotFound {
			t.Fatalf("status %d, want 404 — another user's session must be indistinguishable from a missing one", st)
		}

		if len(listVia(laptop, laptopToken)) != len(sessions) {
			t.Fatal("a session was revoked by a different user")
		}
	})
}

func TestChangePassword(t *testing.T) {
	baseURL, _ := newTestServer(t)
	const email = "c@change.test"
	registerAndLogin(t, baseURL, email)

	caller := newClient(t)
	other := newClient(t)
	callerToken := loginWithJar(t, caller, baseURL, email)
	loginWithJar(t, other, baseURL, email)

	change := func(c *http.Client, token, current, next string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/auth/change-password",
			strings.NewReader(`{"currentPassword":"`+current+`","newPassword":"`+next+`"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// An access token may be stolen; requiring the current password is what
	// stops it becoming a permanent takeover.
	t.Run("wrong current password rejected", func(t *testing.T) {
		if got := change(caller, callerToken, "wrongpassword", "brand-new-password"); got != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", got)
		}
	})

	t.Run("short new password rejected", func(t *testing.T) {
		if got := change(caller, callerToken, "longenoughpw", "short"); got != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", got)
		}
	})

	t.Run("succeeds and evicts other sessions", func(t *testing.T) {
		if got := change(caller, callerToken, "longenoughpw", "a-brand-new-password"); got != http.StatusNoContent {
			t.Fatalf("status %d, want 204", got)
		}

		// The other device's refresh token must be dead.
		if st, _ := post(t, other, baseURL+"/api/auth/refresh", ""); st != http.StatusUnauthorized {
			t.Errorf("other session refresh returned %d, want 401 — it should have been evicted", st)
		}
		// The caller stays signed in.
		if st, _ := post(t, caller, baseURL+"/api/auth/refresh", ""); st != http.StatusOK {
			t.Errorf("caller refresh returned %d, want 200 — the caller should stay signed in", st)
		}
	})

	t.Run("the new password works and the old one does not", func(t *testing.T) {
		st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/login", "",
			map[string]string{"email": email, "password": "longenoughpw"})
		if st != http.StatusUnauthorized {
			t.Errorf("old password login returned %d, want 401", st)
		}
		st, _, _ = doJSON(t, http.MethodPost, baseURL+"/api/auth/login", "",
			map[string]string{"email": email, "password": "a-brand-new-password"})
		if st != http.StatusOK {
			t.Errorf("new password login returned %d, want 200", st)
		}
	})
}

// forgot-password must never reveal whether an address is registered.
func TestForgotPassword_doesNotLeakAccountExistence(t *testing.T) {
	baseURL, _ := newTestServer(t)
	registerAndLogin(t, baseURL, "real@forgot.test")

	before := testMailer.count()

	stKnown, _, bodyKnown := doJSON(t, http.MethodPost, baseURL+"/api/auth/forgot-password", "",
		map[string]string{"email": "real@forgot.test"})
	stUnknown, _, bodyUnknown := doJSON(t, http.MethodPost, baseURL+"/api/auth/forgot-password", "",
		map[string]string{"email": "nobody@forgot.test"})

	if stKnown != http.StatusAccepted || stUnknown != http.StatusAccepted {
		t.Fatalf("statuses differ: known=%d unknown=%d, both must be 202", stKnown, stUnknown)
	}
	if string(bodyKnown) != string(bodyUnknown) {
		t.Fatalf("response bodies differ:\n known:   %s\n unknown: %s", bodyKnown, bodyUnknown)
	}

	// Exactly one mail: the unknown address must not generate one.
	if got := testMailer.count() - before; got != 1 {
		t.Errorf("sent %d mails, want 1 — only the real account should get a link", got)
	}
}

func TestResetPassword_fullJourney(t *testing.T) {
	baseURL, _ := newTestServer(t)
	const email = "r@reset.test"
	registerAndLogin(t, baseURL, email)

	// A live session that must not survive the reset.
	device := newClient(t)
	loginWithJar(t, device, baseURL, email)

	st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/forgot-password", "",
		map[string]string{"email": email})
	if st != http.StatusAccepted {
		t.Fatalf("forgot: %d", st)
	}

	sent, ok := testMailer.last()
	if !ok {
		t.Fatal("no reset mail recorded")
	}
	u, err := url.Parse(sent.URL)
	if err != nil {
		t.Fatalf("reset URL is not parseable: %v", err)
	}
	token := u.Query().Get("token")
	if token == "" {
		t.Fatalf("no token in the reset link: %s", sent.URL)
	}

	t.Run("an invalid token is rejected", func(t *testing.T) {
		if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/reset-password", "",
			map[string]string{"token": "not-a-real-token", "newPassword": "some-new-password"}); st != http.StatusBadRequest {
			t.Errorf("status %d, want 400", st)
		}
	})

	t.Run("the token works once", func(t *testing.T) {
		if st, _, raw := doJSON(t, http.MethodPost, baseURL+"/api/auth/reset-password", "",
			map[string]string{"token": token, "newPassword": "reset-password-value"}); st != http.StatusNoContent {
			t.Fatalf("status %d, %s", st, raw)
		}
	})

	// A link sitting in an inbox must not work a second time.
	t.Run("the token cannot be reused", func(t *testing.T) {
		if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/reset-password", "",
			map[string]string{"token": token, "newPassword": "another-password"}); st != http.StatusBadRequest {
			t.Errorf("status %d, want 400 — reset tokens are single-use", st)
		}
	})

	t.Run("the new password works and the old one does not", func(t *testing.T) {
		if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/login", "",
			map[string]string{"email": email, "password": "longenoughpw"}); st != http.StatusUnauthorized {
			t.Error("the old password still works after a reset")
		}
		if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/login", "",
			map[string]string{"email": email, "password": "reset-password-value"}); st != http.StatusOK {
			t.Error("the new password does not work")
		}
	})

	// Whoever forced the reset must not keep a live session.
	t.Run("every session was revoked", func(t *testing.T) {
		if st, _ := post(t, device, baseURL+"/api/auth/refresh", ""); st != http.StatusUnauthorized {
			t.Errorf("a session survived the reset: refresh returned %d, want 401", st)
		}
	})
}

// Requesting a second link must burn the first.
func TestResetPassword_onlyTheNewestLinkWorks(t *testing.T) {
	baseURL, _ := newTestServer(t)
	const email = "n@reset.test"
	registerAndLogin(t, baseURL, email)

	tokenFrom := func() string {
		t.Helper()
		doJSON(t, http.MethodPost, baseURL+"/api/auth/forgot-password", "", map[string]string{"email": email})
		sent, ok := testMailer.last()
		if !ok {
			t.Fatal("no mail recorded")
		}
		u, _ := url.Parse(sent.URL)
		return u.Query().Get("token")
	}

	first := tokenFrom()
	second := tokenFrom()
	if first == second {
		t.Fatal("two requests produced the same token")
	}

	if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/reset-password", "",
		map[string]string{"token": first, "newPassword": "should-not-work"}); st != http.StatusBadRequest {
		t.Errorf("the superseded token returned %d, want 400", st)
	}
	if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/reset-password", "",
		map[string]string{"token": second, "newPassword": "this-should-work"}); st != http.StatusNoContent {
		t.Errorf("the newest token returned %d, want 204", st)
	}
}

func TestAccount_requiresAuth(t *testing.T) {
	baseURL, _ := newTestServer(t)

	if st, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/auth/me", "", map[string]any{"name": "x"}); st != http.StatusUnauthorized {
		t.Errorf("PATCH /me returned %d, want 401", st)
	}
	if st, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/auth/sessions", "", nil); st != http.StatusUnauthorized {
		t.Errorf("GET /sessions returned %d, want 401", st)
	}
	if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/change-password", "",
		map[string]string{"currentPassword": "x", "newPassword": "yyyyyyyy"}); st != http.StatusUnauthorized {
		t.Errorf("change-password returned %d, want 401", st)
	}
}
