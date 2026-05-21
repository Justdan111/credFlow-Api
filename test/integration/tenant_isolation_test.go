//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestTenantIsolation is the headline security test. Two tenants (Dan and
// Alice) each create one customer and one debt. Then Dan tries every
// cross-tenant attack via the API; each must fail closed. If any pass, the
// multi-tenant guarantee is broken.
func TestTenantIsolation(t *testing.T) {
	baseURL, _ := newTestServer(t)

	tokenDan := registerAndLogin(t, baseURL, "dan@acme.test")
	tokenAlice := registerAndLogin(t, baseURL, "alice@beta.test")

	// Each tenant creates one customer.
	danCustomerID := createCustomer(t, baseURL, tokenDan, "Dan's customer", "dancust@x.test")
	aliceCustomerID := createCustomer(t, baseURL, tokenAlice, "Alice's customer", "alicecust@x.test")

	// Each tenant creates one debt.
	danDebtID := createDebt(t, baseURL, tokenDan, danCustomerID, 1000, "2026-12-01")
	aliceDebtID := createDebt(t, baseURL, tokenAlice, aliceCustomerID, 500, "2026-12-01")

	t.Run("Dan GET Alice's customer => 404", func(t *testing.T) {
		s, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/customers/"+aliceCustomerID, tokenDan, nil)
		if s != http.StatusNotFound {
			t.Errorf("got %d, want 404", s)
		}
	})
	t.Run("Dan PATCH Alice's customer => 404", func(t *testing.T) {
		s, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/customers/"+aliceCustomerID, tokenDan,
			map[string]string{"riskLevel": "high"})
		if s != http.StatusNotFound {
			t.Errorf("got %d, want 404", s)
		}
	})
	t.Run("Dan DELETE Alice's customer => 404", func(t *testing.T) {
		s, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/customers/"+aliceCustomerID, tokenDan, nil)
		if s != http.StatusNotFound {
			t.Errorf("got %d, want 404", s)
		}
	})
	t.Run("Dan creates debt for Alice's customer => 404", func(t *testing.T) {
		s, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/debts", tokenDan, map[string]any{
			"customerId": aliceCustomerID,
			"amount":     1,
			"dueDate":    "2026-12-31",
		})
		if s != http.StatusNotFound {
			t.Errorf("got %d, want 404 — INSERT...WHERE EXISTS guard breached", s)
		}
	})
	t.Run("Dan GET Alice's debt => 404", func(t *testing.T) {
		s, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/debts/"+aliceDebtID, tokenDan, nil)
		if s != http.StatusNotFound {
			t.Errorf("got %d, want 404", s)
		}
	})
	t.Run("Dan mark-paid Alice's debt => 404", func(t *testing.T) {
		s, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/debts/"+aliceDebtID+"/mark-paid", tokenDan, nil)
		if s != http.StatusNotFound {
			t.Errorf("got %d, want 404", s)
		}
	})
	t.Run("Dan PATCH Alice's debt => 404", func(t *testing.T) {
		s, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/debts/"+aliceDebtID, tokenDan,
			map[string]any{"amount": 0.01})
		if s != http.StatusNotFound {
			t.Errorf("got %d, want 404", s)
		}
	})
	t.Run("Dan DELETE Alice's debt => 404", func(t *testing.T) {
		s, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/debts/"+aliceDebtID, tokenDan, nil)
		if s != http.StatusNotFound {
			t.Errorf("got %d, want 404", s)
		}
	})

	// List endpoints must scope to the caller's tenant.
	t.Run("Dan's customer list contains only his customer", func(t *testing.T) {
		s, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/customers", tokenDan, nil)
		if s != http.StatusOK {
			t.Fatalf("list: %d", s)
		}
		var rows []struct{ ID string }
		if err := json.Unmarshal(env.Data, &rows); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(rows) != 1 || rows[0].ID != danCustomerID {
			t.Errorf("expected only Dan's customer, got %+v", rows)
		}
	})
	t.Run("Dan's debt list contains only his debt", func(t *testing.T) {
		s, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/debts", tokenDan, nil)
		if s != http.StatusOK {
			t.Fatalf("list: %d", s)
		}
		var rows []struct{ ID string }
		if err := json.Unmarshal(env.Data, &rows); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(rows) != 1 || rows[0].ID != danDebtID {
			t.Errorf("expected only Dan's debt, got %+v", rows)
		}
	})

	// Alice's data is still pristine after Dan's attacks.
	t.Run("Alice's customer untouched", func(t *testing.T) {
		s, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/customers/"+aliceCustomerID, tokenAlice, nil)
		if s != http.StatusOK {
			t.Fatalf("alice get her own: %d", s)
		}
		var c struct{ Name, RiskLevel string }
		_ = json.Unmarshal(env.Data, &c)
		if c.Name != "Alice's customer" || c.RiskLevel != "low" {
			t.Errorf("customer changed: %+v", c)
		}
	})
	t.Run("Alice's debt untouched", func(t *testing.T) {
		s, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/debts/"+aliceDebtID, tokenAlice, nil)
		if s != http.StatusOK {
			t.Fatalf("alice get her own debt: %d", s)
		}
		var d struct {
			Amount float64
			Status string
		}
		_ = json.Unmarshal(env.Data, &d)
		if d.Amount != 500 || d.Status != "pending" {
			t.Errorf("debt changed: %+v", d)
		}
	})
}

// --- shared resource creators ---

func createCustomer(t *testing.T, baseURL, token, name, email string) string {
	t.Helper()
	status, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/customers", token, map[string]string{
		"name":  name,
		"email": email,
	})
	if status != http.StatusCreated {
		t.Fatalf("create customer: %d, %s", status, raw)
	}
	var c struct{ ID string }
	if err := json.Unmarshal(env.Data, &c); err != nil {
		t.Fatalf("decode customer: %v", err)
	}
	return c.ID
}

func createDebt(t *testing.T, baseURL, token, customerID string, amount float64, dueDate string) string {
	t.Helper()
	status, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/debts", token, map[string]any{
		"customerId": customerID,
		"amount":     amount,
		"dueDate":    dueDate,
	})
	if status != http.StatusCreated {
		t.Fatalf("create debt: %d, %s", status, raw)
	}
	var d struct{ ID string }
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatalf("decode debt: %v", err)
	}
	return d.ID
}
