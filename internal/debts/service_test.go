package debts

import (
	"context"
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
		{"dueDate", "due_date", "ASC", false},
		{"-dueDate", "due_date", "DESC", false},
		{"amount", "amount", "ASC", false},
		{"status", "status", "ASC", false},
		{"password_hash", "", "", true},
		{"due_date", "", "", true}, // SQL name, not API name — must reject
		{"unknown", "", "", true},
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

// Create's validation runs BEFORE any repo call. By passing a service with a
// nil repo and asserting only validation-failure cases here, we exercise the
// validation block in isolation. Any test case that accidentally passes
// validation will panic on the nil repo — which itself signals "this isn't
// a pure validation case."
func TestCreate_validationOnly(t *testing.T) {
	svc := &Service{repo: nil}
	ctx := context.Background()

	tests := []struct {
		name string
		req  CreateRequest
		want string
	}{
		{"missing customerId", CreateRequest{Amount: 100, DueDate: "2026-12-31"}, "customerId"},
		{"zero amount", CreateRequest{CustomerID: "c", Amount: 0, DueDate: "2026-12-31"}, "amount"},
		{"negative amount", CreateRequest{CustomerID: "c", Amount: -1, DueDate: "2026-12-31"}, "amount"},
		{"missing dueDate", CreateRequest{CustomerID: "c", Amount: 100}, "dueDate"},
		{"bad dueDate format", CreateRequest{CustomerID: "c", Amount: 100, DueDate: "31/12/2026"}, "YYYY-MM-DD"},
		{"bad issuedDate format", CreateRequest{CustomerID: "c", Amount: 100, IssuedDate: "31/12/2026", DueDate: "2026-12-31"}, "YYYY-MM-DD"},
		{"due before issued", CreateRequest{CustomerID: "c", Amount: 100, IssuedDate: "2026-12-31", DueDate: "2026-01-01"}, "dueDate cannot be before"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(ctx, "biz", tc.req)
			if err == nil {
				t.Fatal("got nil error, want validation error")
			}
			if !errors.Is(err, ErrValidation) {
				t.Errorf("not ErrValidation: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing %q", err.Error(), tc.want)
			}
		})
	}
}

func TestUpdate_validationOnly(t *testing.T) {
	svc := &Service{repo: nil}
	ctx := context.Background()

	fptr := func(f float64) *float64 { return &f }
	sptr := func(s string) *string { return &s }

	tests := []struct {
		name string
		req  UpdateRequest
		want string
	}{
		{"zero amount", UpdateRequest{Amount: fptr(0)}, "amount"},
		{"negative amount", UpdateRequest{Amount: fptr(-50)}, "amount"},
		{"bad dueDate format", UpdateRequest{DueDate: sptr("nope")}, "YYYY-MM-DD"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Update(ctx, "biz", "id", tc.req)
			if err == nil {
				t.Fatal("got nil error, want validation error")
			}
			if !errors.Is(err, ErrValidation) {
				t.Errorf("not ErrValidation: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing %q", err.Error(), tc.want)
			}
		})
	}
}

func TestList_clampsAndValidatesQuery(t *testing.T) {
	svc := &Service{repo: nil}
	ctx := context.Background()

	t.Run("invalid status rejected before repo call", func(t *testing.T) {
		_, _, err := svc.List(ctx, "biz", ListQuery{Status: "banana"})
		if err == nil || !errors.Is(err, ErrValidation) {
			t.Errorf("invalid status: got %v, want ErrValidation", err)
		}
	})

	t.Run("invalid sort rejected before repo call", func(t *testing.T) {
		_, _, err := svc.List(ctx, "biz", ListQuery{Sort: "password_hash"})
		if err == nil || !errors.Is(err, ErrValidation) {
			t.Errorf("invalid sort: got %v, want ErrValidation", err)
		}
	})
}
