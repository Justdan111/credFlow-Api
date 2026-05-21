package customers

import (
	"errors"
	"strings"
	"testing"
)

func TestParseSort(t *testing.T) {
	tests := []struct {
		input   string
		wantCol string
		wantDir string
		wantErr bool
	}{
		{"", "created_at", "DESC", false},
		{"createdAt", "created_at", "ASC", false},
		{"-createdAt", "created_at", "DESC", false},
		{"name", "name", "ASC", false},
		{"-name", "name", "DESC", false},
		{"riskLevel", "risk_level", "ASC", false},
		{"creditLimit", "credit_limit", "ASC", false},
		// Injection probes — every one must fail closed.
		{"password_hash", "", "", true},
		{"name; DROP TABLE customers", "", "", true},
		{"id) UNION SELECT", "", "", true},
		{"unknown_field", "", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			col, dir, err := parseSort(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got nil error, want rejection of %q", tc.input)
				}
				if !errors.Is(err, ErrValidation) {
					t.Errorf("not ErrValidation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if col != tc.wantCol || dir != tc.wantDir {
				t.Errorf("got (%q, %q), want (%q, %q)", col, dir, tc.wantCol, tc.wantDir)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	valid := CreateRequest{Name: "John", Email: "j@x.test", RiskLevel: "low", CreditLimit: 100}

	tests := []struct {
		name    string
		mutate  func(*CreateRequest)
		wantErr bool
		wantMsg string
	}{
		{"valid passes", func(r *CreateRequest) {}, false, ""},
		{"empty name", func(r *CreateRequest) { r.Name = "" }, true, "name"},
		{"whitespace name", func(r *CreateRequest) { r.Name = "   " }, true, "name"},
		{"bad email", func(r *CreateRequest) { r.Email = "nope" }, true, "email"},
		{"empty email allowed", func(r *CreateRequest) { r.Email = "" }, false, ""},
		{"unknown risk level", func(r *CreateRequest) { r.RiskLevel = "extreme" }, true, "riskLevel"},
		{"empty risk defaults to low", func(r *CreateRequest) { r.RiskLevel = "" }, false, ""},
		{"negative credit limit", func(r *CreateRequest) { r.CreditLimit = -1 }, true, "creditLimit"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			tc.mutate(&req)

			err := validateCreate(&req)
			if tc.wantErr && err == nil {
				t.Fatal("got nil error, want validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("got error %v, want nil", err)
			}
			if err == nil {
				return
			}
			if !errors.Is(err, ErrValidation) {
				t.Errorf("not ErrValidation: %v", err)
			}
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q missing %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestValidateUpdate_pointerSemantics(t *testing.T) {
	// Helper to take a pointer to a literal string without a separate var.
	ptr := func(s string) *string { return &s }
	fptr := func(f float64) *float64 { return &f }

	t.Run("all nil = no-op valid", func(t *testing.T) {
		if err := validateUpdate(&UpdateRequest{}); err != nil {
			t.Errorf("empty update should pass validateUpdate: %v", err)
		}
	})

	t.Run("non-nil empty name fails", func(t *testing.T) {
		err := validateUpdate(&UpdateRequest{Name: ptr("   ")})
		if err == nil || !errors.Is(err, ErrValidation) {
			t.Errorf("empty/whitespace name update should fail: %v", err)
		}
	})

	t.Run("non-nil empty email is allowed (clears email)", func(t *testing.T) {
		// "" means "clear the field" — repo translates to NULL.
		if err := validateUpdate(&UpdateRequest{Email: ptr("")}); err != nil {
			t.Errorf("clearing email should pass validateUpdate: %v", err)
		}
	})

	t.Run("non-nil bad email fails", func(t *testing.T) {
		err := validateUpdate(&UpdateRequest{Email: ptr("nope")})
		if err == nil || !errors.Is(err, ErrValidation) {
			t.Errorf("bad email update should fail: %v", err)
		}
	})

	t.Run("non-nil negative credit limit fails", func(t *testing.T) {
		err := validateUpdate(&UpdateRequest{CreditLimit: fptr(-1)})
		if err == nil || !errors.Is(err, ErrValidation) {
			t.Errorf("negative credit limit update should fail: %v", err)
		}
	})
}
