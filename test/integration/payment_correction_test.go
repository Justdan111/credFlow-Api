//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

type debtView struct {
	ID              string  `json:"id"`
	Amount          float64 `json:"amount"`
	AmountPaid      float64 `json:"amountPaid"`
	AmountRemaining float64 `json:"amountRemaining"`
	Status          string  `json:"status"`
}

func getDebt(t *testing.T, baseURL, token, debtID string) debtView {
	t.Helper()
	status, env, raw := doJSON(t, http.MethodGet, baseURL+"/api/debts/"+debtID, token, nil)
	if status != http.StatusOK {
		t.Fatalf("get debt: %d, %s", status, raw)
	}
	var d debtView
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatalf("decode debt: %v", err)
	}
	return d
}

// TestPaymentCorrection_recomputesTheDebt is the invariant that matters: a
// corrected amount must move the debt between pending, partial and paid, or the
// ledger reports a status the numbers no longer support.
func TestPaymentCorrection_recomputesTheDebt(t *testing.T) {
	baseURL, _ := newTestServer(t)
	owner := registerAndLogin(t, baseURL, "correct@acme.test")
	customerID := createCustomer(t, baseURL, owner, "Corrected Co", "corrected@x.test")
	debtID := createDebt(t, baseURL, owner, customerID, 1000, "2026-12-01")

	paymentID := recordPayment(t, baseURL, owner, customerID, debtID, 400, "")

	if d := getDebt(t, baseURL, owner, debtID); d.Status != "partial" || d.AmountPaid != 400 {
		t.Fatalf("after 400 of 1000: status %q, paid %v", d.Status, d.AmountPaid)
	}

	t.Run("correcting upwards can settle the debt", func(t *testing.T) {
		status, _, raw := doJSON(t, http.MethodPatch, baseURL+"/api/payments/"+paymentID, owner,
			map[string]any{"amount": 1000})
		if status != http.StatusOK {
			t.Fatalf("patch: %d, %s", status, raw)
		}
		d := getDebt(t, baseURL, owner, debtID)
		if d.Status != "paid" || d.AmountPaid != 1000 || d.AmountRemaining != 0 {
			t.Errorf("after correction to 1000: status %q, paid %v, remaining %v",
				d.Status, d.AmountPaid, d.AmountRemaining)
		}
	})

	t.Run("correcting downwards reopens it", func(t *testing.T) {
		status, _, raw := doJSON(t, http.MethodPatch, baseURL+"/api/payments/"+paymentID, owner,
			map[string]any{"amount": 250})
		if status != http.StatusOK {
			t.Fatalf("patch: %d, %s", status, raw)
		}
		d := getDebt(t, baseURL, owner, debtID)
		// The bug this guards against is a debt pinned at "paid" while the
		// money behind it shrinks.
		if d.Status != "partial" || d.AmountPaid != 250 || d.AmountRemaining != 750 {
			t.Errorf("after correction to 250: status %q, paid %v, remaining %v",
				d.Status, d.AmountPaid, d.AmountRemaining)
		}
	})

	t.Run("correcting metadata leaves the amount alone", func(t *testing.T) {
		status, env, raw := doJSON(t, http.MethodPatch, baseURL+"/api/payments/"+paymentID, owner,
			map[string]any{"reference": "TRF-CORRECTED", "method": "bank_transfer"})
		if status != http.StatusOK {
			t.Fatalf("patch: %d, %s", status, raw)
		}
		var p struct {
			Amount    float64 `json:"amount"`
			Method    string  `json:"method"`
			Reference *string `json:"reference"`
		}
		_ = json.Unmarshal(env.Data, &p)
		if p.Amount != 250 {
			t.Errorf("an omitted amount must be left unchanged, got %v", p.Amount)
		}
		if p.Method != "bank_transfer" || p.Reference == nil || *p.Reference != "TRF-CORRECTED" {
			t.Errorf("fields not applied: %+v", p)
		}
	})

	t.Run("invalid corrections are rejected", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"amount": 0},
			{"amount": -5},
			{"method": "carrier-pigeon"},
			{"paidAt": "not-a-date"},
		} {
			status, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/payments/"+paymentID, owner, body)
			if status != http.StatusBadRequest {
				t.Errorf("%v: got %d, want 400", body, status)
			}
		}
	})

	t.Run("a member cannot correct a payment", func(t *testing.T) {
		colleague := invite(t, baseURL, owner, "correct-member@acme.test", "Member", "member")
		sent, _ := testMailer.last()
		memberToken := tokenFromSetupLink(t, baseURL, sent.URL, "correct-member@acme.test", "member-password")
		_ = colleague

		status, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/payments/"+paymentID, memberToken,
			map[string]any{"amount": 999})
		if status != http.StatusForbidden {
			t.Errorf("got %d, want 403", status)
		}
	})
}

func TestPaymentCorrection_isTenantIsolated(t *testing.T) {
	baseURL, _ := newTestServer(t)
	dan := registerAndLogin(t, baseURL, "dan-correct@acme.test")
	alice := registerAndLogin(t, baseURL, "alice-correct@beta.test")

	aliceCustomer := createCustomer(t, baseURL, alice, "Alice Co", "alice-correct-cust@x.test")
	aliceDebt := createDebt(t, baseURL, alice, aliceCustomer, 500, "2026-12-01")
	alicePayment := recordPayment(t, baseURL, alice, aliceCustomer, aliceDebt, 100, "")

	status, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/payments/"+alicePayment, dan,
		map[string]any{"amount": 1})
	if status != http.StatusNotFound {
		t.Errorf("got %d, want 404", status)
	}
}

// TestDebtPayments_listRoute covers the endpoint that was documented from the
// first catalogue but never registered — the frontend filtered the collection
// endpoint to work around it.
func TestDebtPayments_listRoute(t *testing.T) {
	baseURL, _ := newTestServer(t)
	owner := registerAndLogin(t, baseURL, "debtpayments@acme.test")
	customerID := createCustomer(t, baseURL, owner, "Listed Co", "listed@x.test")

	debtID := createDebt(t, baseURL, owner, customerID, 1000, "2026-12-01")
	otherDebtID := createDebt(t, baseURL, owner, customerID, 500, "2026-12-01")

	recordPayment(t, baseURL, owner, customerID, debtID, 100, "")
	recordPayment(t, baseURL, owner, customerID, debtID, 200, "")
	recordPayment(t, baseURL, owner, customerID, otherDebtID, 300, "")

	status, env, raw := doJSON(t, http.MethodGet, baseURL+"/api/debts/"+debtID+"/payments", owner, nil)
	if status != http.StatusOK {
		t.Fatalf("list: %d, %s", status, raw)
	}
	var items []struct {
		DebtID *string `json:"debtId"`
		Amount float64 `json:"amount"`
	}
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d payments, want 2 (%s)", len(items), raw)
	}
	for _, p := range items {
		// The other debt's payment must not appear.
		if p.DebtID == nil || *p.DebtID != debtID {
			t.Errorf("a payment for another debt leaked: %+v", p)
		}
	}

	t.Run("an unknown debt 404s rather than returning an empty list", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodGet,
			baseURL+"/api/debts/00000000-0000-0000-0000-000000000000/payments", owner, nil)
		if status != http.StatusNotFound {
			t.Errorf("got %d, want 404", status)
		}
	})

	t.Run("another tenant's debt 404s", func(t *testing.T) {
		alice := registerAndLogin(t, baseURL, "alice-debtpayments@beta.test")
		status, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/debts/"+debtID+"/payments", alice, nil)
		if status != http.StatusNotFound {
			t.Errorf("got %d, want 404", status)
		}
	})
}
