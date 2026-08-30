package businesses

import (
	"errors"
	"testing"
)

func strp(s string) *string   { return &s }
func f64p(f float64) *float64 { return &f }

func TestValidateUpdate(t *testing.T) {
	tests := []struct {
		name    string
		req     UpdateRequest
		wantErr bool
	}{
		{"empty request is fine", UpdateRequest{}, false},
		{"valid name", UpdateRequest{Name: strp("Lagos Traders")}, false},
		{"blank name rejected", UpdateRequest{Name: strp("   ")}, true},
		{"supported currency", UpdateRequest{Currency: strp("GHS")}, false},
		{"lowercase currency accepted", UpdateRequest{Currency: strp("ghs")}, false},
		{"unknown currency rejected", UpdateRequest{Currency: strp("EUR")}, true},
		{"zero target allowed", UpdateRequest{MonthlyCollectionTarget: f64p(0)}, false},
		{"negative target rejected", UpdateRequest{MonthlyCollectionTarget: f64p(-1)}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUpdate(tc.req)
			if tc.wantErr && !errors.Is(err, ErrValidation) {
				t.Fatalf("got %v, want a validation error", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateComplete(t *testing.T) {
	cust := func(name, email string) *struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Phone string `json:"phone"`
	} {
		return &struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Phone string `json:"phone"`
		}{Name: name, Email: email}
	}
	debt := func(amount float64, due string) *struct {
		Amount  float64 `json:"amount"`
		DueDate string  `json:"dueDate"`
	} {
		return &struct {
			Amount  float64 `json:"amount"`
			DueDate string  `json:"dueDate"`
		}{Amount: amount, DueDate: due}
	}

	tests := []struct {
		name    string
		req     CompleteRequest
		wantErr bool
	}{
		{"profile only", CompleteRequest{Industry: "retail", Size: "1-10"}, false},
		{"with customer", CompleteRequest{Customer: cust("ABC", "a@b.test")}, false},
		{"customer without email", CompleteRequest{Customer: cust("ABC", "")}, false},
		{"full payload", CompleteRequest{Customer: cust("ABC", "a@b.test"), Debt: debt(1000, "2026-12-01")}, false},

		// A debt needs somebody to owe it.
		{"debt without customer", CompleteRequest{Debt: debt(1000, "2026-12-01")}, true},
		{"blank customer name", CompleteRequest{Customer: cust("  ", "a@b.test")}, true},
		{"bad customer email", CompleteRequest{Customer: cust("ABC", "not-an-email")}, true},
		{"zero debt amount", CompleteRequest{Customer: cust("ABC", ""), Debt: debt(0, "2026-12-01")}, true},
		{"negative debt amount", CompleteRequest{Customer: cust("ABC", ""), Debt: debt(-5, "2026-12-01")}, true},
		{"missing due date", CompleteRequest{Customer: cust("ABC", ""), Debt: debt(1000, "")}, true},
		{"unknown currency", CompleteRequest{Currency: "EUR"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateComplete(tc.req)
			if tc.wantErr && !errors.Is(err, ErrValidation) {
				t.Fatalf("got %v, want a validation error", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// The Go list must match migration 0007's CHECK constraint, or a value that
// passes validation fails at the database with a 500.
func TestSupportedCurrenciesMatchTheSchema(t *testing.T) {
	want := []string{"NGN", "GHS", "KES", "ZAR", "USD"}
	if len(supportedCurrencies) != len(want) {
		t.Fatalf("got %d currencies, want %d", len(supportedCurrencies), len(want))
	}
	for _, c := range want {
		if !supportedCurrencies[c] {
			t.Errorf("%s missing from supportedCurrencies", c)
		}
	}
}
