//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type note struct {
	ID         string `json:"id"`
	CustomerID string `json:"customerId"`
	AuthorName string `json:"authorName"`
	Body       string `json:"body"`
	Channel    string `json:"channel"`
}

func TestNotes_journey(t *testing.T) {
	baseURL, _ := newTestServer(t)
	owner := registerAndLogin(t, baseURL, "notes@acme.test")
	customerID := createCustomer(t, baseURL, owner, "Noted Co", "noted@x.test")

	t.Run("a new customer has no notes", func(t *testing.T) {
		status, env, raw := doJSON(t, http.MethodGet,
			baseURL+"/api/customers/"+customerID+"/notes", owner, nil)
		if status != http.StatusOK {
			t.Fatalf("list: %d, %s", status, raw)
		}
		var items []note
		if err := json.Unmarshal(env.Data, &items); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(items) != 0 {
			t.Errorf("got %d notes, want 0", len(items))
		}
	})

	var created note
	t.Run("recording a call", func(t *testing.T) {
		status, env, raw := doJSON(t, http.MethodPost,
			baseURL+"/api/customers/"+customerID+"/notes", owner,
			map[string]string{"body": "Rang twice, promised Friday", "channel": "call"})
		if status != http.StatusCreated {
			t.Fatalf("create: %d, %s", status, raw)
		}
		if err := json.Unmarshal(env.Data, &created); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if created.Channel != "call" {
			t.Errorf("channel: got %q, want call", created.Channel)
		}
		// Attribution is captured at write time so it survives the author
		// later being removed from the team.
		if created.AuthorName == "" {
			t.Error("note has no author name")
		}
	})

	t.Run("the channel defaults to note", func(t *testing.T) {
		status, env, _ := doJSON(t, http.MethodPost,
			baseURL+"/api/customers/"+customerID+"/notes", owner,
			map[string]string{"body": "Left a message"})
		if status != http.StatusCreated {
			t.Fatalf("create: %d", status)
		}
		var n note
		_ = json.Unmarshal(env.Data, &n)
		if n.Channel != "note" {
			t.Errorf("channel: got %q, want note", n.Channel)
		}
	})

	t.Run("an empty body is rejected", func(t *testing.T) {
		// The database CHECK would reject this too; the service turns it into a
		// 400 with a useful message rather than a 500.
		status, _, _ := doJSON(t, http.MethodPost,
			baseURL+"/api/customers/"+customerID+"/notes", owner,
			map[string]string{"body": "   "})
		if status != http.StatusBadRequest {
			t.Errorf("got %d, want 400", status)
		}
	})

	t.Run("an unknown channel is rejected", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodPost,
			baseURL+"/api/customers/"+customerID+"/notes", owner,
			map[string]string{"body": "hi", "channel": "carrier-pigeon"})
		if status != http.StatusBadRequest {
			t.Errorf("got %d, want 400", status)
		}
	})

	t.Run("notes for an unknown customer 404", func(t *testing.T) {
		// An empty list would read as "this customer has no notes", which is a
		// different and misleading answer.
		status, _, _ := doJSON(t, http.MethodGet,
			baseURL+"/api/customers/00000000-0000-0000-0000-000000000000/notes", owner, nil)
		if status != http.StatusNotFound {
			t.Errorf("got %d, want 404", status)
		}
	})

	t.Run("filtering by channel", func(t *testing.T) {
		status, env, raw := doJSON(t, http.MethodGet,
			baseURL+"/api/customers/"+customerID+"/notes?channel=call", owner, nil)
		if status != http.StatusOK {
			t.Fatalf("list: %d, %s", status, raw)
		}
		var items []note
		_ = json.Unmarshal(env.Data, &items)
		if len(items) != 1 || items[0].Channel != "call" {
			t.Errorf("got %d items, want 1 call (%s)", len(items), raw)
		}
	})

	t.Run("a member cannot retract a note", func(t *testing.T) {
		colleague := invite(t, baseURL, owner, "notes-member@acme.test", "Member", "member")
		sent, _ := testMailer.last()
		memberToken := tokenFromSetupLink(t, baseURL, sent.URL, "notes-member@acme.test", "member-password")
		_ = colleague

		status, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/notes/"+created.ID, memberToken, nil)
		if status != http.StatusForbidden {
			t.Errorf("got %d, want 403", status)
		}
	})

	t.Run("an owner can retract a note", func(t *testing.T) {
		status, _, raw := doJSON(t, http.MethodDelete, baseURL+"/api/notes/"+created.ID, owner, nil)
		if status != http.StatusNoContent {
			t.Fatalf("delete: %d, %s", status, raw)
		}
		status, _, _ = doJSON(t, http.MethodDelete, baseURL+"/api/notes/"+created.ID, owner, nil)
		if status != http.StatusNotFound {
			t.Errorf("second delete: got %d, want 404", status)
		}
	})
}

func TestNotes_areTenantIsolated(t *testing.T) {
	baseURL, _ := newTestServer(t)
	dan := registerAndLogin(t, baseURL, "dan-notes@acme.test")
	alice := registerAndLogin(t, baseURL, "alice-notes@beta.test")

	aliceCustomer := createCustomer(t, baseURL, alice, "Alice Co", "alice-notes-cust@x.test")
	status, env, raw := doJSON(t, http.MethodPost,
		baseURL+"/api/customers/"+aliceCustomer+"/notes", alice,
		map[string]string{"body": "Alice's private note"})
	if status != http.StatusCreated {
		t.Fatalf("alice create note: %d, %s", status, raw)
	}
	var aliceNote note
	_ = json.Unmarshal(env.Data, &aliceNote)

	t.Run("Dan cannot read notes on Alice's customer", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodGet,
			baseURL+"/api/customers/"+aliceCustomer+"/notes", dan, nil)
		if status != http.StatusNotFound {
			t.Errorf("got %d, want 404", status)
		}
	})

	t.Run("Dan cannot attach a note to Alice's customer", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodPost,
			baseURL+"/api/customers/"+aliceCustomer+"/notes", dan,
			map[string]string{"body": "injected"})
		if status != http.StatusNotFound {
			t.Errorf("got %d, want 404", status)
		}
	})

	t.Run("Dan cannot retract Alice's note", func(t *testing.T) {
		status, _, _ := doJSON(t, http.MethodDelete, baseURL+"/api/notes/"+aliceNote.ID, dan, nil)
		if status != http.StatusNotFound {
			t.Errorf("got %d, want 404", status)
		}
	})
}

type searchResults struct {
	Query     string `json:"query"`
	Customers []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"customers"`
	Debts []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"debts"`
	Payments []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"payments"`
}

func doSearch(t *testing.T, baseURL, token, term string) searchResults {
	t.Helper()
	status, env, raw := doJSON(t, http.MethodGet, baseURL+"/api/search?q="+term, token, nil)
	if status != http.StatusOK {
		t.Fatalf("search %q: %d, %s", term, status, raw)
	}
	var out searchResults
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	return out
}

func TestSearch(t *testing.T) {
	baseURL, _ := newTestServer(t)
	owner := registerAndLogin(t, baseURL, "search@acme.test")

	customerID := createCustomer(t, baseURL, owner, "Bello Traders", "bello@x.test")
	debtID := createDebt(t, baseURL, owner, customerID, 250000, "2026-12-01")
	if s, _, b := doJSON(t, http.MethodPost, baseURL+"/api/payments", owner, map[string]any{
		"customerId": customerID, "debtId": debtID, "amount": 50000,
		"method": "bank_transfer", "reference": "TRF-778",
	}); s != http.StatusCreated {
		t.Fatalf("create payment: %d, %s", s, b)
	}

	t.Run("finds a customer by name", func(t *testing.T) {
		got := doSearch(t, baseURL, owner, "bello")
		if len(got.Customers) != 1 || got.Customers[0].ID != customerID {
			t.Errorf("customers: got %+v", got.Customers)
		}
	})

	t.Run("finds debts and payments through the customer name", func(t *testing.T) {
		// Somebody typing a person's name into the header box wants their
		// debts, not only the customer record.
		got := doSearch(t, baseURL, owner, "Bello")
		if len(got.Debts) != 1 {
			t.Errorf("debts: got %+v", got.Debts)
		}
		if len(got.Payments) != 1 {
			t.Errorf("payments: got %+v", got.Payments)
		}
	})

	t.Run("finds a payment by reference", func(t *testing.T) {
		got := doSearch(t, baseURL, owner, "TRF-778")
		if len(got.Payments) != 1 {
			t.Errorf("payments: got %+v", got.Payments)
		}
	})

	t.Run("search is case insensitive", func(t *testing.T) {
		if got := doSearch(t, baseURL, owner, "BELLO"); len(got.Customers) != 1 {
			t.Errorf("customers: got %+v", got.Customers)
		}
	})

	t.Run("wildcards in the term are literal", func(t *testing.T) {
		// "o%" is chosen because the two behaviours differ: unescaped, LIKE
		// reads it as "contains o followed by anything" and matches "Bello
		// Traders"; escaped, it requires a literal percent sign and matches
		// nothing. The user must get what they typed.
		if got := doSearch(t, baseURL, owner, "o%25"); len(got.Customers) != 0 {
			t.Errorf("%% behaved as a wildcard: matched %d customers", len(got.Customers))
		}
		// Same for "_", which LIKE reads as "any single character": unescaped,
		// "b_l" would match the "bel" inside "Bello".
		if got := doSearch(t, baseURL, owner, "b_l"); len(got.Customers) != 0 {
			t.Errorf("_ behaved as a wildcard: matched %d customers", len(got.Customers))
		}
		// And the escaping must not break an ordinary search.
		if got := doSearch(t, baseURL, owner, "bel"); len(got.Customers) != 1 {
			t.Errorf("escaping broke a plain search: matched %d customers", len(got.Customers))
		}
	})

	t.Run("a one-character term is rejected", func(t *testing.T) {
		// Below two characters almost every row matches: a slow query returning
		// noise rather than an answer.
		status, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/search?q=b", owner, nil)
		if status != http.StatusBadRequest {
			t.Errorf("got %d, want 400", status)
		}
	})

	t.Run("results never cross tenants", func(t *testing.T) {
		alice := registerAndLogin(t, baseURL, "alice-search@beta.test")
		got := doSearch(t, baseURL, alice, "Bello")
		if len(got.Customers)+len(got.Debts)+len(got.Payments) != 0 {
			t.Errorf("another tenant's records leaked into search: %+v", got)
		}
	})

	t.Run("no match returns empty arrays, not null", func(t *testing.T) {
		// A JSON null would force every client to null-check before iterating.
		status, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/search?q=zzzznothing", owner, nil)
		if status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
		if !strings.Contains(string(env.Data), `"customers":[]`) {
			t.Errorf("expected an empty array, got %s", env.Data)
		}
	})
}
