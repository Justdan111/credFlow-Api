//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestPayments_partialToPaidTransition covers the debt status state machine:
// 0  payments => pending
// >0 but < amount => partial
// >= amount => paid (with paid_at set)
func TestPayments_partialToPaidTransition(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "owner1@payments.test")

	customerID := createCustomer(t, baseURL, token, "C", "c@payments.test")
	debtID := createDebt(t, baseURL, token, customerID, 1000, "2026-12-31")

	// Initial state.
	d := mustGetDebt(t, baseURL, token, debtID)
	if d.Status != "pending" || d.AmountPaid != 0 || d.AmountRemaining != 1000 {
		t.Fatalf("initial: %+v", d)
	}

	// Partial payment.
	recordPayment(t, baseURL, token, customerID, debtID, 300, "")
	d = mustGetDebt(t, baseURL, token, debtID)
	if d.Status != "partial" || d.AmountPaid != 300 || d.AmountRemaining != 700 {
		t.Fatalf("after $300: %+v", d)
	}

	// Finishing payment.
	recordPayment(t, baseURL, token, customerID, debtID, 700, "")
	d = mustGetDebt(t, baseURL, token, debtID)
	if d.Status != "paid" || d.AmountPaid != 1000 || d.AmountRemaining != 0 {
		t.Fatalf("after $700: %+v", d)
	}
	if d.PaidAt == nil {
		t.Error("paid_at must be set when debt becomes paid")
	}
}

func TestPayments_overpaymentMovesToPaid(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "owner@overpay.test")

	customerID := createCustomer(t, baseURL, token, "C", "c@overpay.test")
	debtID := createDebt(t, baseURL, token, customerID, 1000, "2026-12-31")

	recordPayment(t, baseURL, token, customerID, debtID, 1500, "")

	d := mustGetDebt(t, baseURL, token, debtID)
	if d.Status != "paid" || d.AmountRemaining != 0 {
		t.Errorf("overpayment: %+v", d)
	}
	if d.AmountPaid != 1500 {
		t.Errorf("amount paid should reflect actual sum: %v", d.AmountPaid)
	}
}

// Idempotency: two POSTs with the same key must produce ONE payment.
// The second response should be the same payment, with status 200 (replay)
// rather than 201 (created).
func TestPayments_idempotencyKeyReturnsSamePayment(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "owner@idempotent.test")

	customerID := createCustomer(t, baseURL, token, "C", "c@idempotent.test")
	debtID := createDebt(t, baseURL, token, customerID, 1000, "2026-12-31")

	body := map[string]any{
		"customerId":     customerID,
		"debtId":         debtID,
		"amount":         400,
		"method":         "card",
		"idempotencyKey": "client-uuid-abc-123",
	}

	// First submit.
	status1, env1, raw1 := doJSON(t, http.MethodPost, baseURL+"/api/payments", token, body)
	if status1 != http.StatusCreated {
		t.Fatalf("first submit: %d, %s", status1, raw1)
	}
	var first struct{ ID string }
	if err := json.Unmarshal(env1.Data, &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}

	// Replay.
	status2, env2, _ := doJSON(t, http.MethodPost, baseURL+"/api/payments", token, body)
	if status2 != http.StatusOK {
		t.Errorf("replay status: got %d, want 200 (was %d on first)", status2, status1)
	}
	var second struct{ ID string }
	if err := json.Unmarshal(env2.Data, &second); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("idempotency broken: %s vs %s", first.ID, second.ID)
	}

	// The debt should only have been credited once.
	d := mustGetDebt(t, baseURL, token, debtID)
	if d.AmountPaid != 400 {
		t.Errorf("debt double-credited: amountPaid = %v (want 400)", d.AmountPaid)
	}
}

// Voiding a payment must roll the debt's status back appropriately.
func TestPayments_voidRollsBackDebtStatus(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "owner@void.test")

	customerID := createCustomer(t, baseURL, token, "C", "c@void.test")
	debtID := createDebt(t, baseURL, token, customerID, 1000, "2026-12-31")

	paymentID := recordPayment(t, baseURL, token, customerID, debtID, 1000, "")

	// Debt should be paid.
	d := mustGetDebt(t, baseURL, token, debtID)
	if d.Status != "paid" {
		t.Fatalf("pre-void status: %v", d.Status)
	}

	// Void the payment.
	st, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/payments/"+paymentID, token, nil)
	if st != http.StatusNoContent {
		t.Fatalf("delete payment: %d", st)
	}

	// Debt should be back to pending; remaining back to amount.
	d = mustGetDebt(t, baseURL, token, debtID)
	if d.Status != "pending" {
		t.Errorf("post-void status: got %v, want pending", d.Status)
	}
	if d.AmountPaid != 0 || d.AmountRemaining != 1000 {
		t.Errorf("post-void totals: paid=%v remaining=%v", d.AmountPaid, d.AmountRemaining)
	}
	if d.PaidAt != nil {
		t.Error("paid_at should clear when debt drops out of paid status")
	}
}

// Mark-paid is an administrative close. Voiding a payment against such a debt
// must NOT reopen it — administrative decisions outrank arithmetic.
func TestPayments_voidPreservesAdministrativeMarkPaid(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "owner@admin.test")

	customerID := createCustomer(t, baseURL, token, "C", "c@admin.test")
	debtID := createDebt(t, baseURL, token, customerID, 1000, "2026-12-31")

	paymentID := recordPayment(t, baseURL, token, customerID, debtID, 200, "")

	// Now admin-close it.
	st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/debts/"+debtID+"/mark-paid", token, nil)
	if st != http.StatusOK {
		t.Fatalf("mark-paid: %d", st)
	}

	// Void the payment.
	st, _, _ = doJSON(t, http.MethodDelete, baseURL+"/api/payments/"+paymentID, token, nil)
	if st != http.StatusNoContent {
		t.Fatalf("delete payment: %d", st)
	}

	// Debt must remain paid — the administrative decision survives the void.
	d := mustGetDebt(t, baseURL, token, debtID)
	if d.Status != "paid" {
		t.Errorf("admin-closed debt was reopened by void: status=%v", d.Status)
	}
	if d.AmountRemaining != 0 {
		t.Errorf("admin-closed debt: amountRemaining=%v, want 0", d.AmountRemaining)
	}
}

// Cross-tenant: Dan must not be able to record a payment against Alice's debt.
func TestPayments_crossTenantBlocked(t *testing.T) {
	baseURL, _ := newTestServer(t)
	tokenDan := registerAndLogin(t, baseURL, "dan@xtenant.test")
	tokenAlice := registerAndLogin(t, baseURL, "alice@xtenant.test")

	aliceCustomerID := createCustomer(t, baseURL, tokenAlice, "Alice's C", "c@aliceX.test")
	aliceDebtID := createDebt(t, baseURL, tokenAlice, aliceCustomerID, 500, "2026-12-31")

	// Dan tries to record a payment against Alice's debt via /api/payments.
	st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/payments", tokenDan, map[string]any{
		"customerId": aliceCustomerID,
		"debtId":     aliceDebtID,
		"amount":     50,
	})
	if st != http.StatusNotFound {
		t.Errorf("got %d, want 404 — INSERT...WHERE EXISTS breached", st)
	}

	// Dan tries the nested route too.
	st, _, _ = doJSON(t, http.MethodPost, baseURL+"/api/debts/"+aliceDebtID+"/payments", tokenDan,
		map[string]any{"amount": 50})
	if st != http.StatusNotFound {
		t.Errorf("nested route: got %d, want 404", st)
	}

	// Alice's debt is untouched.
	d := mustGetDebt(t, baseURL, tokenAlice, aliceDebtID)
	if d.AmountPaid != 0 {
		t.Errorf("alice debt tampered: paid=%v", d.AmountPaid)
	}
}

// Validation: amount=0, bad method, missing customerId.
func TestPayments_validation(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "owner@validate.test")
	customerID := createCustomer(t, baseURL, token, "C", "c@validate.test")

	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing customerId", map[string]any{"amount": 100}},
		{"zero amount", map[string]any{"customerId": customerID, "amount": 0}},
		{"negative amount", map[string]any{"customerId": customerID, "amount": -1}},
		{"unknown method", map[string]any{"customerId": customerID, "amount": 100, "method": "bitcoin"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/payments", token, tc.body)
			if st != http.StatusBadRequest {
				t.Errorf("got %d, want 400", st)
			}
		})
	}
}

// --- shared helpers for payments tests ---

// debtRow is just the parts of a debt these tests need to read.
type debtRow struct {
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	Amount          float64  `json:"amount"`
	AmountPaid      float64  `json:"amountPaid"`
	AmountRemaining float64  `json:"amountRemaining"`
	PaidAt          *string  `json:"paidAt,omitempty"`
}

func mustGetDebt(t *testing.T, baseURL, token, debtID string) debtRow {
	t.Helper()
	st, env, raw := doJSON(t, http.MethodGet, baseURL+"/api/debts/"+debtID, token, nil)
	if st != http.StatusOK {
		t.Fatalf("get debt: %d, %s", st, raw)
	}
	var d debtRow
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatalf("decode debt: %v", err)
	}
	return d
}

func recordPayment(t *testing.T, baseURL, token, customerID, debtID string, amount float64, idempotencyKey string) string {
	t.Helper()
	body := map[string]any{
		"customerId": customerID,
		"debtId":     debtID,
		"amount":     amount,
	}
	if idempotencyKey != "" {
		body["idempotencyKey"] = idempotencyKey
	}
	st, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/payments", token, body)
	if st != http.StatusCreated {
		t.Fatalf("record payment: %d, %s", st, raw)
	}
	var p struct{ ID string }
	if err := json.Unmarshal(env.Data, &p); err != nil {
		t.Fatalf("decode payment: %v", err)
	}
	return p.ID
}
