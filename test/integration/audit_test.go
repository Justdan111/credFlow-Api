//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

type auditEntry struct {
	ID         string         `json:"id"`
	ActorEmail string         `json:"actorEmail"`
	ActorName  string         `json:"actorName"`
	Action     string         `json:"action"`
	EntityType string         `json:"entityType"`
	EntityID   *string        `json:"entityId"`
	Metadata   map[string]any `json:"metadata"`
	IP         *string        `json:"ip"`
}

func auditLog(t *testing.T, baseURL, token, query string) []auditEntry {
	t.Helper()
	url := baseURL + "/api/audit-logs"
	if query != "" {
		url += "?" + query
	}
	status, env, raw := doJSON(t, http.MethodGet, url, token, nil)
	if status != http.StatusOK {
		t.Fatalf("read audit log: %d, %s", status, raw)
	}
	var entries []auditEntry
	if err := json.Unmarshal(env.Data, &entries); err != nil {
		t.Fatalf("decode audit log: %v", err)
	}
	return entries
}

func actionsIn(entries []auditEntry) map[string]auditEntry {
	byAction := make(map[string]auditEntry, len(entries))
	for _, e := range entries {
		byAction[e.Action] = e
	}
	return byAction
}

// TestAudit_recordsDestructiveAndFinancialActions is the point of the trail:
// after the fact, somebody must be able to answer "who voided that payment?".
func TestAudit_recordsDestructiveAndFinancialActions(t *testing.T) {
	baseURL, _ := newTestServer(t)
	owner := registerAndLogin(t, baseURL, "auditor@acme.test")

	customerID := createCustomer(t, baseURL, owner, "Audited Co", "audited@x.test")
	debtID := createDebt(t, baseURL, owner, customerID, 1000, "2026-12-01")

	// Record a payment, correct it, then void it.
	status, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/payments", owner, map[string]any{
		"customerId": customerID, "debtId": debtID, "amount": 400, "method": "cash",
	})
	if status != http.StatusCreated {
		t.Fatalf("create payment: %d, %s", status, raw)
	}
	var payment struct{ ID string }
	_ = json.Unmarshal(env.Data, &payment)

	if s, _, b := doJSON(t, http.MethodPatch, baseURL+"/api/payments/"+payment.ID, owner,
		map[string]any{"amount": 500}); s != http.StatusOK {
		t.Fatalf("update payment: %d, %s", s, b)
	}
	if s, _, b := doJSON(t, http.MethodDelete, baseURL+"/api/payments/"+payment.ID, owner, nil); s != http.StatusNoContent {
		t.Fatalf("void payment: %d, %s", s, b)
	}

	// Mark the debt paid, then delete it and the customer.
	if s, _, b := doJSON(t, http.MethodPost, baseURL+"/api/debts/"+debtID+"/mark-paid", owner, nil); s != http.StatusOK {
		t.Fatalf("mark paid: %d, %s", s, b)
	}
	if s, _, b := doJSON(t, http.MethodDelete, baseURL+"/api/debts/"+debtID, owner, nil); s != http.StatusNoContent {
		t.Fatalf("delete debt: %d, %s", s, b)
	}
	if s, _, b := doJSON(t, http.MethodDelete, baseURL+"/api/customers/"+customerID, owner, nil); s != http.StatusNoContent {
		t.Fatalf("delete customer: %d, %s", s, b)
	}

	entries := auditLog(t, baseURL, owner, "pageSize=100")
	byAction := actionsIn(entries)

	for _, want := range []string{
		"payment.created", "payment.updated", "payment.voided",
		"debt.marked_paid", "debt.deleted", "customer.deleted",
	} {
		if _, ok := byAction[want]; !ok {
			t.Errorf("no audit entry for %q (recorded: %v)", want, keysOf(byAction))
		}
	}

	t.Run("entries name the actor", func(t *testing.T) {
		e := byAction["payment.voided"]
		if e.ActorEmail != "auditor@acme.test" {
			t.Errorf("actor email: got %q", e.ActorEmail)
		}
		if e.ActorName == "" {
			t.Error("actor name is empty")
		}
	})

	t.Run("a voided payment records how much", func(t *testing.T) {
		e := byAction["payment.voided"]
		// An entry that cannot say what was voided answers none of the
		// questions asked when the books do not balance.
		if amount, ok := e.Metadata["amount"].(float64); !ok || amount != 500 {
			t.Errorf("metadata amount: got %v, want 500", e.Metadata["amount"])
		}
		if e.EntityID == nil || *e.EntityID != payment.ID {
			t.Errorf("entity id: got %v, want %s", e.EntityID, payment.ID)
		}
	})

	t.Run("a deleted customer records who it was", func(t *testing.T) {
		e := byAction["customer.deleted"]
		if name, _ := e.Metadata["name"].(string); name != "Audited Co" {
			t.Errorf("metadata name: got %v", e.Metadata["name"])
		}
	})

	t.Run("filtering by action narrows the trail", func(t *testing.T) {
		only := auditLog(t, baseURL, owner, "action=payment.voided")
		if len(only) != 1 {
			t.Fatalf("got %d entries, want 1", len(only))
		}
		if only[0].Action != "payment.voided" {
			t.Errorf("wrong entry returned: %s", only[0].Action)
		}
	})

	t.Run("reads never appear in the trail", func(t *testing.T) {
		// A trail that logs every GET is noise nobody reads, and it would turn
		// the audit screen into a record of when colleagues were at their desks.
		for _, e := range entries {
			if e.Action == "customer.viewed" || e.Action == "payment.viewed" {
				t.Errorf("a read was recorded: %s", e.Action)
			}
		}
	})
}

func TestAudit_isTenantIsolatedAndPrivileged(t *testing.T) {
	baseURL, _ := newTestServer(t)
	dan := registerAndLogin(t, baseURL, "dan-audit@acme.test")
	alice := registerAndLogin(t, baseURL, "alice-audit@beta.test")

	// Alice deletes one of her own customers, producing an entry in her trail.
	aliceCustomer := createCustomer(t, baseURL, alice, "Alice Co", "alice-audit-cust@x.test")
	if s, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/customers/"+aliceCustomer, alice, nil); s != http.StatusNoContent {
		t.Fatalf("alice delete: %d", s)
	}

	t.Run("Dan's trail does not contain Alice's actions", func(t *testing.T) {
		for _, e := range auditLog(t, baseURL, dan, "pageSize=100") {
			if e.ActorEmail == "alice-audit@beta.test" {
				t.Fatalf("another tenant's audit entry leaked: %+v", e)
			}
		}
	})

	t.Run("a member cannot read the trail", func(t *testing.T) {
		colleague := invite(t, baseURL, alice, "alice-member@beta.test", "Member", "member")
		sent, _ := testMailer.last()
		memberToken := tokenFromSetupLink(t, baseURL, sent.URL, "alice-member@beta.test", "member-password")
		_ = colleague

		// The trail records when the owner works and what they touch; that is
		// not something a member needs to study.
		status, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/audit-logs", memberToken, nil)
		if status != http.StatusForbidden {
			t.Errorf("got %d, want 403", status)
		}
	})
}

func TestAudit_rejectsMalformedFilters(t *testing.T) {
	baseURL, _ := newTestServer(t)
	owner := registerAndLogin(t, baseURL, "audit-filter@acme.test")

	// A query-string id is not covered by the path-parameter middleware, so it
	// is validated in the handler — otherwise it reaches Postgres and 500s.
	status, _, raw := doJSON(t, http.MethodGet, baseURL+"/api/audit-logs?entityId=not-a-uuid", owner, nil)
	if status != http.StatusBadRequest {
		t.Errorf("entityId: got %d, want 400 (%s)", status, raw)
	}
	status, _, raw = doJSON(t, http.MethodGet, baseURL+"/api/audit-logs?actorId=12345", owner, nil)
	if status != http.StatusBadRequest {
		t.Errorf("actorId: got %d, want 400 (%s)", status, raw)
	}
}

func keysOf(m map[string]auditEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
