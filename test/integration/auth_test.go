//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAuth_registerLoginMe(t *testing.T) {
	baseURL, _ := newTestServer(t)

	// 1. Register a new user.
	registerBody := map[string]string{
		"businessName": "Acme Co",
		"email":        "user1@acme.test",
		"password":     "longenoughpw",
		"name":         "User One",
	}
	status, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/auth/register", "", registerBody)
	if status != http.StatusCreated {
		t.Fatalf("register: %d, %s", status, raw)
	}

	var registered struct {
		User     struct{ ID, Email, Name, Role string } `json:"user"`
		Business struct{ ID, Name string }              `json:"business"`
		Token    string                                 `json:"token"`
	}
	if err := json.Unmarshal(env.Data, &registered); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if registered.User.ID == "" || registered.Business.ID == "" || registered.Token == "" {
		t.Fatalf("incomplete register response: %+v", registered)
	}
	if registered.User.Role != "owner" {
		t.Errorf("first user role: got %q, want owner", registered.User.Role)
	}

	// 2. Duplicate registration must 409.
	dupStatus, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/register", "", registerBody)
	if dupStatus != http.StatusConflict {
		t.Errorf("duplicate register: got %d, want 409", dupStatus)
	}

	// 3. Login with correct password.
	loginStatus, loginEnv, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/login", "", map[string]string{
		"email":    "user1@acme.test",
		"password": "longenoughpw",
	})
	if loginStatus != http.StatusOK {
		t.Fatalf("login: %d", loginStatus)
	}
	var loggedIn struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(loginEnv.Data, &loggedIn); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if loggedIn.Token == "" {
		t.Fatal("login: token missing")
	}

	// 4. /me with the token returns the user.
	meStatus, meEnv, _ := doJSON(t, http.MethodGet, baseURL+"/api/auth/me", loggedIn.Token, nil)
	if meStatus != http.StatusOK {
		t.Fatalf("me: %d", meStatus)
	}
	var me struct {
		User struct{ Email string } `json:"user"`
	}
	if err := json.Unmarshal(meEnv.Data, &me); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if me.User.Email != "user1@acme.test" {
		t.Errorf("me email: got %q", me.User.Email)
	}

	// 5. /me without a token must 401.
	noAuthStatus, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/auth/me", "", nil)
	if noAuthStatus != http.StatusUnauthorized {
		t.Errorf("me without token: got %d, want 401", noAuthStatus)
	}
}

// User-enumeration safety: wrong email and wrong password must produce
// identical responses, so attackers can't probe which emails are registered.
func TestAuth_loginDoesNotLeakUserExistence(t *testing.T) {
	baseURL, _ := newTestServer(t)

	// Register a real user so we have a real email to test against.
	_ = registerAndLogin(t, baseURL, "real@acme.test")

	// Wrong password for a real email.
	statusA, envA, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/login", "", map[string]string{
		"email":    "real@acme.test",
		"password": "wrong-password-here",
	})

	// Any password for a non-existent email.
	statusB, envB, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/login", "", map[string]string{
		"email":    "nobody@nowhere.test",
		"password": "anything-here",
	})

	if statusA != http.StatusUnauthorized || statusB != http.StatusUnauthorized {
		t.Fatalf("expected both 401, got A=%d B=%d", statusA, statusB)
	}
	if envA.Error == nil || envB.Error == nil {
		t.Fatal("expected error bodies on both")
	}
	if envA.Error.Message != envB.Error.Message {
		t.Errorf("error messages differ — user enumeration leak:\n  wrong-password: %q\n  wrong-email   : %q",
			envA.Error.Message, envB.Error.Message)
	}
}
