package users

import (
	"errors"
	"testing"

	"github.com/Justdan111/credflow-api/internal/auth"
)

func TestCheckGrant(t *testing.T) {
	tests := []struct {
		name      string
		actor     string
		target    string
		wantError bool
	}{
		{"owner grants owner", auth.RoleOwner, auth.RoleOwner, false},
		{"owner grants admin", auth.RoleOwner, auth.RoleAdmin, false},
		{"owner grants member", auth.RoleOwner, auth.RoleMember, false},
		{"admin grants admin", auth.RoleAdmin, auth.RoleAdmin, false},
		{"admin grants member", auth.RoleAdmin, auth.RoleMember, false},
		{"member grants member", auth.RoleMember, auth.RoleMember, false},

		// The rule that keeps admin meaningfully weaker than owner. Without it
		// an admin mints an owner and inherits every power they were not given.
		{"admin cannot grant owner", auth.RoleAdmin, auth.RoleOwner, true},
		{"member cannot grant admin", auth.RoleMember, auth.RoleAdmin, true},
		{"member cannot grant owner", auth.RoleMember, auth.RoleOwner, true},

		// An unknown actor role ranks zero, so it can grant nothing.
		{"unknown actor role grants nothing", "superuser", auth.RoleMember, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkGrant(tc.actor, tc.target)
			if tc.wantError {
				if !errors.Is(err, ErrEscalation) {
					t.Fatalf("got %v, want ErrEscalation", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateInvite(t *testing.T) {
	valid := InviteRequest{Email: "new@x.test", Name: "New Person", Role: auth.RoleMember}

	tests := []struct {
		name     string
		mutate   func(*InviteRequest)
		actor    string
		wantErr  error
		wantRole string
	}{
		{"accepts a well-formed invite", nil, auth.RoleOwner, nil, auth.RoleMember},
		{
			name:     "defaults to the least privileged role",
			mutate:   func(r *InviteRequest) { r.Role = "" },
			actor:    auth.RoleOwner,
			wantRole: auth.RoleMember,
		},
		{
			name:     "trims surrounding whitespace",
			mutate:   func(r *InviteRequest) { r.Email = "  new@x.test  "; r.Name = "  New Person " },
			actor:    auth.RoleOwner,
			wantRole: auth.RoleMember,
		},
		{
			name:    "rejects a missing name",
			mutate:  func(r *InviteRequest) { r.Name = "   " },
			actor:   auth.RoleOwner,
			wantErr: ErrValidation,
		},
		{
			name:    "rejects a malformed email",
			mutate:  func(r *InviteRequest) { r.Email = "not-an-email" },
			actor:   auth.RoleOwner,
			wantErr: ErrValidation,
		},
		{
			name:    "rejects an unknown role",
			mutate:  func(r *InviteRequest) { r.Role = "superuser" },
			actor:   auth.RoleOwner,
			wantErr: ErrValidation,
		},
		{
			name:    "an admin cannot invite an owner",
			mutate:  func(r *InviteRequest) { r.Role = auth.RoleOwner },
			actor:   auth.RoleAdmin,
			wantErr: ErrEscalation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			if tc.mutate != nil {
				tc.mutate(&req)
			}

			err := validateInvite(&req, tc.actor)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("got %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.Role != tc.wantRole {
				t.Errorf("role: got %q, want %q", req.Role, tc.wantRole)
			}
			if req.Email != "new@x.test" || req.Name != "New Person" {
				t.Errorf("not normalised: email %q, name %q", req.Email, req.Name)
			}
		})
	}
}

func TestRoleRank_ordersPrivilegeAscending(t *testing.T) {
	// The comparisons throughout this package assume this ordering; if a role
	// is ever added in the wrong position every guard silently changes meaning.
	if !(roleRank[auth.RoleMember] < roleRank[auth.RoleAdmin] &&
		roleRank[auth.RoleAdmin] < roleRank[auth.RoleOwner]) {
		t.Fatalf("roles are not ordered member < admin < owner: %v", roleRank)
	}
	if roleRank["unknown"] != 0 {
		t.Error("an unrecognised role must rank below every real one")
	}
}

func TestRandomPasswordHash_isUniqueAndNotTheInput(t *testing.T) {
	// The invited account must be unusable until the invitee redeems their
	// link: no default password, and no two invitations sharing one.
	first, err := randomPasswordHash()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := randomPasswordHash()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first == second {
		t.Error("two invitations produced the same password hash")
	}
	if first == "" {
		t.Error("empty password hash")
	}
}
