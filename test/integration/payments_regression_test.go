//go:build integration

// Regression tests for two bugs fixed on fix/payments-idempotency-and-void.
// Both were reproducible on main and against a live server.
package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

// payOnce records a payment and returns its id plus the HTTP status. Unlike the
// shared recordPayment helper it tolerates 200, which is what an idempotent
// replay correctly returns — the shared helper insists on 201.
func payOnce(t *testing.T, baseURL, token, customerID, debtID string, amount float64, key string) (string, int) {
	t.Helper()
	body := map[string]any{"customerId": customerID, "debtId": debtID, "amount": amount}
	if key != "" {
		body["idempotencyKey"] = key
	}
	st, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/payments", token, body)
	if st != http.StatusCreated && st != http.StatusOK {
		t.Fatalf("record payment: status %d, body %s", st, raw)
	}
	var p struct{ ID string }
	if err := json.Unmarshal(env.Data, &p); err != nil {
		t.Fatalf("decode payment: %v", err)
	}
	return p.ID, st
}

// Bug 2: voiding one of several payments must recompute to the real state, not
// pin the debt to whatever status it happened to hold.
func TestPayments_voidOneOfManyRecomputesToPartial(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "multi@void.test")

	customerID := createCustomer(t, baseURL, token, "C", "c@void.test")
	debtID := createDebt(t, baseURL, token, customerID, 1000, "2026-12-31")

	keep := recordPayment(t, baseURL, token, customerID, debtID, 300, "")
	drop := recordPayment(t, baseURL, token, customerID, debtID, 700, "")

	// Both payments together settle the debt.
	if d := mustGetDebt(t, baseURL, token, debtID); d.Status != "paid" {
		t.Fatalf("before void: status = %q, want paid", d.Status)
	}

	if st, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/payments/"+drop, token, nil); st != http.StatusNoContent {
		t.Fatalf("void: status %d, want 204", st)
	}

	// 300 of 1000 remains paid, so the debt is partial — not paid, not pending.
	d := mustGetDebt(t, baseURL, token, debtID)
	if d.Status != "partial" {
		t.Errorf("status = %q, want partial", d.Status)
	}
	if d.AmountPaid != 300 {
		t.Errorf("amountPaid = %v, want 300", d.AmountPaid)
	}
	if d.AmountRemaining != 700 {
		t.Errorf("amountRemaining = %v, want 700 — this was forced to 0 by the bug", d.AmountRemaining)
	}
	if d.PaidAt != nil {
		t.Errorf("paidAt = %v, want nil once the debt is no longer settled", *d.PaidAt)
	}

	_ = keep
}

// Voiding every payment must return the debt all the way to pending.
func TestPayments_voidAllReturnsDebtToPending(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "all@void.test")

	customerID := createCustomer(t, baseURL, token, "C", "c@allvoid.test")
	debtID := createDebt(t, baseURL, token, customerID, 500, "2026-12-31")
	paymentID := recordPayment(t, baseURL, token, customerID, debtID, 500, "")

	if st, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/payments/"+paymentID, token, nil); st != http.StatusNoContent {
		t.Fatalf("void: status %d", st)
	}

	d := mustGetDebt(t, baseURL, token, debtID)
	if d.Status != "pending" {
		t.Errorf("status = %q, want pending", d.Status)
	}
	if d.AmountPaid != 0 {
		t.Errorf("amountPaid = %v, want 0", d.AmountPaid)
	}
	if d.AmountRemaining != 500 {
		t.Errorf("amountRemaining = %v, want 500", d.AmountRemaining)
	}
}

// Bug 1: a replayed idempotency key must return the original payment and must
// not double-count against the debt.
func TestPayments_idempotentReplayDoesNotDoubleCount(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "idem@replay.test")

	customerID := createCustomer(t, baseURL, token, "C", "c@replay.test")
	debtID := createDebt(t, baseURL, token, customerID, 1000, "2026-12-31")

	const key = "replay-key-1"
	first, st := payOnce(t, baseURL, token, customerID, debtID, 250, key)
	if st != http.StatusCreated {
		t.Fatalf("first request: status %d, want 201", st)
	}

	for i := 0; i < 3; i++ {
		again, st := payOnce(t, baseURL, token, customerID, debtID, 250, key)
		if again != first {
			t.Fatalf("replay %d returned payment %q, want the original %q", i+1, again, first)
		}
		// A replay is not a creation; it must not report 201.
		if st != http.StatusOK {
			t.Errorf("replay %d: status %d, want 200", i+1, st)
		}
	}

	// Four requests, one payment: the debt must show 250, not 1000.
	d := mustGetDebt(t, baseURL, token, debtID)
	if d.AmountPaid != 250 {
		t.Errorf("amountPaid = %v, want 250 — replays must not double-count", d.AmountPaid)
	}
	if d.Status != "partial" {
		t.Errorf("status = %q, want partial", d.Status)
	}
}

// The transaction must still be healthy after a replay, so a different payment
// can be recorded in a later request without error.
func TestPayments_replayLeavesTransactionUsable(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "tx@replay.test")

	customerID := createCustomer(t, baseURL, token, "C", "c@tx.test")
	debtID := createDebt(t, baseURL, token, customerID, 1000, "2026-12-31")

	payOnce(t, baseURL, token, customerID, debtID, 100, "tx-key")
	payOnce(t, baseURL, token, customerID, debtID, 100, "tx-key") // replay

	// A fresh key must still work after the savepoint-rollback path ran.
	other, st := payOnce(t, baseURL, token, customerID, debtID, 400, "tx-key-2")
	if other == "" || st != http.StatusCreated {
		t.Fatalf("payment after a replay: id=%q status=%d, want a new payment with 201", other, st)
	}

	d := mustGetDebt(t, baseURL, token, debtID)
	if d.AmountPaid != 500 {
		t.Errorf("amountPaid = %v, want 500 (100 + 400)", d.AmountPaid)
	}
}
