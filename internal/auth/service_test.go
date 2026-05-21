package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateRegister(t *testing.T) {
	valid := RegisterRequest{
		BusinessName: "Acme Co",
		Email:        "dan@acme.test",
		Password:     "longenoughpw",
		Name:         "Dan",
	}

	tests := []struct {
		name    string
		mutate  func(*RegisterRequest)
		wantErr bool
		wantMsg string // substring of the error message; "" = don't check
	}{
		{
			name:    "valid request passes",
			mutate:  func(r *RegisterRequest) {},
			wantErr: false,
		},
		{
			name:    "empty businessName",
			mutate:  func(r *RegisterRequest) { r.BusinessName = "" },
			wantErr: true,
			wantMsg: "businessName",
		},
		{
			name:    "whitespace-only businessName trimmed to empty",
			mutate:  func(r *RegisterRequest) { r.BusinessName = "   " },
			wantErr: true,
			wantMsg: "businessName",
		},
		{
			name:    "empty name",
			mutate:  func(r *RegisterRequest) { r.Name = "" },
			wantErr: true,
			wantMsg: "name",
		},
		{
			name:    "bad email format",
			mutate:  func(r *RegisterRequest) { r.Email = "not-an-email" },
			wantErr: true,
			wantMsg: "email",
		},
		{
			name:    "password too short",
			mutate:  func(r *RegisterRequest) { r.Password = "short" },
			wantErr: true,
			wantMsg: "password",
		},
		{
			name:    "password too long (bcrypt 72-byte limit)",
			mutate:  func(r *RegisterRequest) { r.Password = strings.Repeat("a", 73) },
			wantErr: true,
			wantMsg: "password",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			tc.mutate(&req)

			err := validateRegister(req)
			if tc.wantErr && err == nil {
				t.Fatal("got nil error, want validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("got error %v, want nil", err)
			}
			if err == nil {
				return
			}
			// Every validation error must be matchable as ErrValidation —
			// handler maps that sentinel to 400.
			if !errors.Is(err, ErrValidation) {
				t.Errorf("error not in ErrValidation chain: %v", err)
			}
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantMsg)
			}
		})
	}
}
