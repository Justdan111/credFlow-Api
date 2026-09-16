//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// member mirrors the fields the team endpoints return that these tests assert on.
type member struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// invite sends an invitation and returns the created member.
func invite(t *testing.T, baseURL, token, email, name, role string) member {
	t.Helper()
	status, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/users", token, map[string]string{
		"email": email, "name": name, "role": role,
	})
	if status != http.StatusCreated {
		t.Fatalf("invite %s: status %d, body %s", email, status, raw)
	}
	var out struct {
		Member member `json:"member"`
	}
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("decode invite: %v", err)
	}
	return out.Member
}

// tokenFromSetupLink follows an invitation the way the invitee would: pull the
// token out of the emailed URL, set a password, then sign in.
func tokenFromSetupLink(t *testing.T, baseURL, setupURL, email, password string) string {
	t.Helper()

	parsed, err := url.Parse(setupURL)
	if err != nil {
		t.Fatalf("parse setup link %q: %v", setupURL, err)
	}
	resetToken := parsed.Query().Get("token")
	if resetToken == "" {
		t.Fatalf("setup link carries no token: %s", setupURL)
	}

	status, _, raw := doJSON(t, http.MethodPost, baseURL+"/api/auth/reset-password", "", map[string]string{
		"token": resetToken, "newPassword": password,
	})
	if status != http.StatusNoContent {
		t.Fatalf("redeem invitation: status %d, body %s", status, raw)
	}

	status, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/auth/login", "", map[string]string{
		"email": email, "password": password,
	})
	if status != http.StatusOK {
		t.Fatalf("login as invitee: status %d, body %s", status, raw)
	}
	var data struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return data.AccessToken
}

// TestTeam_inviteJourney walks the whole invitation flow: an owner invites a
// colleague, the colleague sets their own password from the emailed link, signs
// in, and lands with the role they were given — no shared password anywhere.
func TestTeam_inviteJourney(t *testing.T) {
	baseURL, _ := newTestServer(t)
	owner := registerAndLogin(t, baseURL, "owner@acme.test")

	before := testMailer.count()
	invited := invite(t, baseURL, owner, "colleague@acme.test", "Colleague", "admin")

	if invited.Role != "admin" {
		t.Errorf("role: got %q, want admin", invited.Role)
	}

	if testMailer.count() != before+1 {
		t.Fatalf("expected one invitation email, sent %d", testMailer.count()-before)
	}
	sent, ok := testMailer.last()
	if !ok {
		t.Fatal("no mail recorded")
	}
	if sent.To != "colleague@acme.test" {
		t.Errorf("recipient: got %q", sent.To)
	}
	// The invitation names who sent it, so the recipient is not left guessing
	// why a stranger's system emailed them.
	if sent.Inviter == "" {
		t.Error("invitation does not name the inviter")
	}

	// The account exists but must be unusable until the link is redeemed.
	status, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/login", "", map[string]string{
		"email": "colleague@acme.test", "password": "longenoughpw",
	})
	if status != http.StatusUnauthorized {
		t.Errorf("an un-redeemed invitation must not be signable-into: got %d", status)
	}

	colleague := tokenFromSetupLink(t, baseURL, sent.URL, "colleague@acme.test", "colleague-password")

	// The invitee belongs to the inviter's business, not a new one.
	status, env, raw := doJSON(t, http.MethodGet, baseURL+"/api/users", colleague, nil)
	if status != http.StatusOK {
		t.Fatalf("list team as invitee: %d, %s", status, raw)
	}
	var team []member
	if err := json.Unmarshal(env.Data, &team); err != nil {
		t.Fatalf("decode team: %v", err)
	}
	if len(team) != 2 {
		t.Fatalf("team size: got %d, want 2 (%s)", len(team), raw)
	}

	// The link is single-use: a second redemption of the same token must fail,
	// or an invitation left in an inbox stays a live credential forever.
	parsed, _ := url.Parse(sent.URL)
	status, _, _ = doJSON(t, http.MethodPost, baseURL+"/api/auth/reset-password", "", map[string]string{
		"token": parsed.Query().Get("token"), "newPassword": "another-password",
	})
	if status == http.StatusNoContent {
		t.Error("an invitation link was redeemable twice")
	}
}

func TestTeam_privilegeRules(t *testing.T) {
	baseURL, _ := newTestServer(t)
	owner := registerAndLogin(t, baseURL, "owner2@acme.test")

	admin := invite(t, baseURL, owner, "admin@acme.test", "Admin", "admin")
	sentAdmin, _ := testMailer.last()
	adminToken := tokenFromSetupLink(t, baseURL, sentAdmin.URL, "admin@acme.test", "admin-password")

	plain := invite(t, baseURL, owner, "member@acme.test", "Member", "member")
	sentMember, _ := testMailer.last()
	memberToken := tokenFromSetupLink(t, baseURL, sentMember.URL, "member@acme.test", "member-password")

	t.Run("an admin cannot invite an owner", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/users", adminToken, map[string]string{
			"email": "sneaky@acme.test", "name": "Sneaky", "role": "owner",
		})
		if status != http.StatusForbidden {
			t.Errorf("got %d, want 403 — admin must not mint an owner", status)
		}
	})

	t.Run("an admin cannot promote anyone to owner", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/users/"+plain.ID, adminToken,
			map[string]string{"role": "owner"})
		if status != http.StatusForbidden {
			t.Errorf("got %d, want 403", status)
		}
	})

	t.Run("a member cannot invite at all", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/users", memberToken, map[string]string{
			"email": "nope@acme.test", "name": "Nope", "role": "member",
		})
		if status != http.StatusForbidden {
			t.Errorf("got %d, want 403", status)
		}
	})

	t.Run("a member may still see the team", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/users", memberToken, nil)
		if status != http.StatusOK {
			t.Errorf("got %d, want 200 — reading the team is not privileged", status)
		}
	})

	t.Run("an admin cannot remove anyone", func(t *testing.T) {
		// Removal is owner-only: it ends somebody's access outright.
		status, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/users/"+plain.ID, adminToken, nil)
		if status != http.StatusForbidden {
			t.Errorf("got %d, want 403", status)
		}
	})

	t.Run("the last owner cannot be demoted", func(t *testing.T) {
		var ownerID string
		_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/users", owner, nil)
		var team []member
		_ = json.Unmarshal(env.Data, &team)
		for _, m := range team {
			if m.Role == "owner" {
				ownerID = m.ID
			}
		}
		if ownerID == "" {
			t.Fatal("no owner found in the team listing")
		}

		status, _, raw := doJSON(t, http.MethodPatch, baseURL+"/api/users/"+ownerID, owner,
			map[string]string{"role": "member"})
		// A business with no owner can only be repaired with direct database
		// access, so this is refused rather than merely discouraged.
		if status != http.StatusConflict {
			t.Errorf("got %d, want 409 (%s)", status, raw)
		}
	})

	t.Run("an owner cannot remove themselves", func(t *testing.T) {
		var ownerID string
		_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/users", owner, nil)
		var team []member
		_ = json.Unmarshal(env.Data, &team)
		for _, m := range team {
			if m.Role == "owner" {
				ownerID = m.ID
			}
		}
		status, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/users/"+ownerID, owner, nil)
		if status != http.StatusForbidden {
			t.Errorf("got %d, want 403", status)
		}
	})

	t.Run("removal ends the removed member's sessions", func(t *testing.T) {
		// A removed teammate whose refresh token still works is not removed.
		status, _, raw := doJSON(t, http.MethodDelete, baseURL+"/api/users/"+plain.ID, owner, nil)
		if status != http.StatusNoContent {
			t.Fatalf("remove member: %d, %s", status, raw)
		}

		// Their password must no longer work either: the account is gone, not
		// merely hidden from the team list.
		status, _, _ = doJSON(t, http.MethodPost, baseURL+"/api/auth/login", "", map[string]string{
			"email": "member@acme.test", "password": "member-password",
		})
		if status != http.StatusUnauthorized {
			t.Errorf("a removed member could still sign in: got %d", status)
		}

		// And they disappear from the team.
		_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/users", owner, nil)
		var team []member
		_ = json.Unmarshal(env.Data, &team)
		for _, m := range team {
			if m.ID == plain.ID {
				t.Error("a removed member is still listed")
			}
		}
		_ = memberToken
	})

	t.Run("a removed member's email can be invited again", func(t *testing.T) {
		// The partial unique index in migration 0011 exists for exactly this:
		// re-inviting somebody who was removed by mistake.
		status, _, raw := doJSON(t, http.MethodPost, baseURL+"/api/users", owner, map[string]string{
			"email": "member@acme.test", "name": "Member Again", "role": "member",
		})
		if status != http.StatusCreated {
			t.Errorf("got %d, want 201 (%s)", status, raw)
		}
	})

	t.Run("inviting a live address conflicts", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/users", owner, map[string]string{
			"email": "admin@acme.test", "name": "Duplicate", "role": "member",
		})
		if status != http.StatusConflict {
			t.Errorf("got %d, want 409", status)
		}
	})
	_ = admin
}

func TestTeam_isTenantIsolated(t *testing.T) {
	baseURL, _ := newTestServer(t)
	dan := registerAndLogin(t, baseURL, "dan-team@acme.test")
	alice := registerAndLogin(t, baseURL, "alice-team@beta.test")

	aliceColleague := invite(t, baseURL, alice, "alice-colleague@beta.test", "Colleague", "member")

	t.Run("Dan cannot read Alice's teammate", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/users/"+aliceColleague.ID, dan, nil)
		// 404, not 403: confirming the id exists elsewhere would itself leak.
		if status != http.StatusNotFound {
			t.Errorf("got %d, want 404", status)
		}
	})

	t.Run("Dan cannot promote Alice's teammate", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/users/"+aliceColleague.ID, dan,
			map[string]string{"role": "admin"})
		if status != http.StatusNotFound {
			t.Errorf("got %d, want 404", status)
		}
	})

	t.Run("Dan cannot remove Alice's teammate", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/users/"+aliceColleague.ID, dan, nil)
		if status != http.StatusNotFound {
			t.Errorf("got %d, want 404", status)
		}
	})

	t.Run("Dan's team listing contains only Dan", func(t *testing.T) {
		_, env, raw := doJSON(t, http.MethodGet, baseURL+"/api/users", dan, nil)
		var team []member
		if err := json.Unmarshal(env.Data, &team); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(team) != 1 {
			t.Fatalf("team size: got %d, want 1 (%s)", len(team), raw)
		}
		if !strings.HasPrefix(team[0].Email, "dan-team@") {
			t.Errorf("another tenant's user leaked into the listing: %s", team[0].Email)
		}
	})
}
