package payments

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseSort(t *testing.T) {
	tests := []struct {
		input   string
		wantCol string
		wantDir string
		wantErr bool
	}{
		{"", "paid_at", "DESC", false},
		{"paidAt", "paid_at", "ASC", false},
		{"-paidAt", "paid_at", "DESC", false},
		{"amount", "amount", "ASC", false},
		{"-amount", "amount", "DESC", false},
		{"createdAt", "created_at", "ASC", false},
		{"method", "method", "ASC", false},
		// Adversarial / unknown
		{"paid_at", "", "", true}, // SQL name, not API name
		{"idempotency_key", "", "", true},
		{"name; DROP TABLE payments", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			col, dir, err := parseSort(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want rejection of %q", tc.input)
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
	valid := CreateRequest{CustomerID: "cust", Amount: 100, Method: "cash"}

	tests := []struct {
		name    string
		mutate  func(*CreateRequest)
		wantErr bool
		wantMsg string
	}{
		{"valid passes", func(r *CreateRequest) {}, false, ""},
		{"missing customerId", func(r *CreateRequest) { r.CustomerID = "" }, true, "customerId"},
		{"whitespace customerId", func(r *CreateRequest) { r.CustomerID = "   " }, true, "customerId"},
		{"zero amount", func(r *CreateRequest) { r.Amount = 0 }, true, "amount"},
		{"negative amount", func(r *CreateRequest) { r.Amount = -1 }, true, "amount"},
		{"empty method allowed (defaults to cash)", func(r *CreateRequest) { r.Method = "" }, false, ""},
		{"unknown method", func(r *CreateRequest) { r.Method = "bitcoin" }, true, "method"},
		{"all valid methods", func(r *CreateRequest) { r.Method = "bank_transfer" }, false, ""},
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

func TestParsePaidAt(t *testing.T) {
	t.Run("empty defaults to now", func(t *testing.T) {
		before := time.Now()
		got, err := parsePaidAt("")
		after := time.Now()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Before(before) || got.After(after) {
			t.Errorf("default not now-ish: got %v, range [%v, %v]", got, before, after)
		}
	})
	t.Run("valid RFC3339", func(t *testing.T) {
		got, err := parsePaidAt("2026-05-22T10:00:00Z")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Year() != 2026 || got.Month() != time.May || got.Day() != 22 {
			t.Errorf("wrong parse: %v", got)
		}
	})
	t.Run("invalid format rejected", func(t *testing.T) {
		_, err := parsePaidAt("2026-05-22")
		if err == nil || !errors.Is(err, ErrValidation) {
			t.Errorf("expected ErrValidation, got %v", err)
		}
	})
}
