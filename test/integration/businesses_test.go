//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
)

type businessBody struct {
	ID                      string   `json:"id"`
	Name                    string   `json:"name"`
	Industry                *string  `json:"industry"`
	Size                    *string  `json:"size"`
	Currency                string   `json:"currency"`
	MonthlyCollectionTarget *float64 `json:"monthlyCollectionTarget"`
	CurrencyLocked          bool     `json:"currencyLocked"`
	OnboardingCompleted     bool     `json:"onboardingCompleted"`
}

func getBusiness(t *testing.T, baseURL, token string) businessBody {
	t.Helper()
	st, env, raw := doJSON(t, http.MethodGet, baseURL+"/api/businesses/current", token, nil)
	if st != http.StatusOK {
		t.Fatalf("get business: %d, %s", st, raw)
	}
	var b businessBody
	decode(t, env.Data, &b)
	return b
}

func TestBusiness_getCurrentDefaults(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "b@profile.test")

	b := getBusiness(t, baseURL, token)

	if b.Currency != "NGN" {
		t.Errorf("currency = %q, want NGN by default", b.Currency)
	}
	if b.MonthlyCollectionTarget != nil {
		t.Errorf("target = %v, want nil until set", *b.MonthlyCollectionTarget)
	}
	// No debts yet, so the currency is still changeable.
	if b.CurrencyLocked {
		t.Error("currencyLocked = true on a business with no financial records")
	}
	if b.OnboardingCompleted {
		t.Error("onboardingCompleted = true for a brand-new business")
	}
}

func TestBusiness_partialUpdate(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "b@update.test")

	t.Run("updates only what is sent", func(t *testing.T) {
		st, env, raw := doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", token,
			map[string]any{"industry": "retail"})
		if st != http.StatusOK {
			t.Fatalf("status %d, %s", st, raw)
		}
		var b businessBody
		decode(t, env.Data, &b)

		if b.Industry == nil || *b.Industry != "retail" {
			t.Errorf("industry = %v, want retail", b.Industry)
		}
		// Name was not in the payload and must be untouched.
		if b.Name == "" {
			t.Error("name was cleared by a partial update")
		}
	})

	t.Run("sets the collection target", func(t *testing.T) {
		_, env, _ := doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", token,
			map[string]any{"monthlyCollectionTarget": 500000})
		var b businessBody
		decode(t, env.Data, &b)

		if b.MonthlyCollectionTarget == nil || *b.MonthlyCollectionTarget != 500000 {
			t.Fatalf("target = %v, want 500000", b.MonthlyCollectionTarget)
		}
	})

	// An explicit null must clear the target, distinct from omitting the key.
	t.Run("explicit null clears the target", func(t *testing.T) {
		_, env, _ := doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", token,
			map[string]any{"monthlyCollectionTarget": nil})
		var b businessBody
		decode(t, env.Data, &b)

		if b.MonthlyCollectionTarget != nil {
			t.Fatalf("target = %v, want nil after an explicit null", *b.MonthlyCollectionTarget)
		}
	})

	t.Run("rejects bad input", func(t *testing.T) {
		cases := []map[string]any{
			{"currency": "EUR"},
			{"monthlyCollectionTarget": -1},
			{"name": "   "},
		}
		for _, body := range cases {
			if st, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", token, body); st != http.StatusBadRequest {
				t.Errorf("body %v returned %d, want 400", body, st)
			}
		}
	})
}

// The headline rule: currency is free until money exists, then locked.
func TestBusiness_currencyLocksOnceDebtsExist(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "b@currency.test")

	t.Run("changeable while there are no debts", func(t *testing.T) {
		st, env, raw := doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", token,
			map[string]any{"currency": "GHS"})
		if st != http.StatusOK {
			t.Fatalf("status %d, %s", st, raw)
		}
		var b businessBody
		decode(t, env.Data, &b)
		if b.Currency != "GHS" {
			t.Fatalf("currency = %q, want GHS", b.Currency)
		}
		if b.CurrencyLocked {
			t.Error("currencyLocked = true with no financial records")
		}
	})

	customerID := createCustomer(t, baseURL, token, "C", "c@currency.test")
	createDebt(t, baseURL, token, customerID, 1000, "2027-12-31")

	t.Run("locked once a debt exists", func(t *testing.T) {
		st, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", token,
			map[string]any{"currency": "KES"})
		if st != http.StatusConflict {
			t.Fatalf("status %d, want 409 — changing currency would reinterpret every stored amount", st)
		}

		// The stored value must be unchanged after the rejection.
		if b := getBusiness(t, baseURL, token); b.Currency != "GHS" {
			t.Errorf("currency = %q after a rejected change, want GHS", b.Currency)
		}
	})

	t.Run("currencyLocked is reported so the UI can disable the selector", func(t *testing.T) {
		if b := getBusiness(t, baseURL, token); !b.CurrencyLocked {
			t.Error("currencyLocked = false despite a debt existing")
		}
	})

	// Setting the same currency is a no-op, not a change, so it must succeed.
	t.Run("re-sending the same currency is allowed", func(t *testing.T) {
		if st, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", token,
			map[string]any{"currency": "GHS"}); st != http.StatusOK {
			t.Errorf("status %d, want 200 — setting the current value is not a change", st)
		}
	})

	// Other fields stay editable after the currency locks.
	t.Run("other fields remain editable", func(t *testing.T) {
		if st, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", token,
			map[string]any{"industry": "wholesale"}); st != http.StatusOK {
			t.Errorf("status %d, want 200", st)
		}
	})
}

// A payment alone must lock the currency too, not only a debt.
func TestBusiness_currencyLocksOnPaymentsAlone(t *testing.T) {
	baseURL, pool := newTestServer(t)
	token := registerAndLogin(t, baseURL, "b@paylock.test")

	customerID := createCustomer(t, baseURL, token, "C", "c@paylock.test")
	debtID := createDebt(t, baseURL, token, customerID, 1000, "2027-12-31")
	recordPayment(t, baseURL, token, customerID, debtID, 100, "")

	// Remove the debt, leaving only the payment behind.
	if _, err := pool.Exec(context.Background(),
		`UPDATE debts SET deleted_at = NOW() WHERE id = $1`, debtID); err != nil {
		t.Fatalf("soft-delete debt: %v", err)
	}

	st, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", token,
		map[string]any{"currency": "ZAR"})
	if st != http.StatusConflict {
		t.Fatalf("status %d, want 409 — a payment alone must still lock the currency", st)
	}
}

func TestBusiness_updateRequiresElevatedRole(t *testing.T) {
	baseURL, pool := newTestServer(t)
	const email = "b@role.test"
	token := registerAndLogin(t, baseURL, email)

	// Owner can edit.
	if st, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", token,
		map[string]any{"industry": "retail"}); st != http.StatusOK {
		t.Fatalf("owner got %d, want 200", st)
	}

	// Demote to member and log in again for a token carrying the new role.
	if _, err := pool.Exec(context.Background(),
		`UPDATE users SET role = 'member' WHERE email = $1`, email); err != nil {
		t.Fatalf("demote: %v", err)
	}
	st, env, _ := doJSON(t, http.MethodPost, baseURL+"/api/auth/login", "",
		map[string]string{"email": email, "password": "longenoughpw"})
	if st != http.StatusOK {
		t.Fatalf("re-login: %d", st)
	}
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	decode(t, env.Data, &login)

	t.Run("member cannot edit", func(t *testing.T) {
		if st, _, _ := doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", login.AccessToken,
			map[string]any{"industry": "wholesale"}); st != http.StatusForbidden {
			t.Errorf("member got %d, want 403", st)
		}
	})

	t.Run("member can still read", func(t *testing.T) {
		if st, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/businesses/current", login.AccessToken, nil); st != http.StatusOK {
			t.Errorf("member read got %d, want 200", st)
		}
	})
}

func TestOnboarding_statusIsDerivedFromRealData(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "b@status.test")

	type status struct {
		Completed   bool   `json:"completed"`
		CurrentStep string `json:"currentStep"`
		Steps       struct {
			Business bool `json:"business"`
			Customer bool `json:"customer"`
			Debt     bool `json:"debt"`
		} `json:"steps"`
	}
	get := func() status {
		t.Helper()
		st, env, raw := doJSON(t, http.MethodGet, baseURL+"/api/onboarding/status", token, nil)
		if st != http.StatusOK {
			t.Fatalf("status: %d, %s", st, raw)
		}
		var s status
		decode(t, env.Data, &s)
		return s
	}

	t.Run("fresh business starts at the business step", func(t *testing.T) {
		s := get()
		if s.Completed || s.CurrentStep != "business" {
			t.Fatalf("got %+v, want incomplete at the business step", s)
		}
	})

	doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", token, map[string]any{"industry": "retail"})

	t.Run("advances once the profile is set", func(t *testing.T) {
		s := get()
		if !s.Steps.Business || s.CurrentStep != "customer" {
			t.Fatalf("got %+v, want the customer step", s)
		}
	})

	customerID := createCustomer(t, baseURL, token, "C", "c@status.test")

	t.Run("advances once a customer exists", func(t *testing.T) {
		s := get()
		if !s.Steps.Customer || s.CurrentStep != "debt" {
			t.Fatalf("got %+v, want the debt step", s)
		}
	})

	createDebt(t, baseURL, token, customerID, 500, "2027-12-31")

	// Steps reflect real records, but the flow is only "completed" once the
	// user actually finishes it.
	t.Run("debt step is satisfied but the flow is not auto-completed", func(t *testing.T) {
		s := get()
		if !s.Steps.Debt {
			t.Error("debt step false despite a debt existing")
		}
		if s.Completed {
			t.Error("completed = true without calling /onboarding/complete")
		}
	})
}

func TestOnboarding_completeIsAtomic(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "b@complete.test")

	body := map[string]any{
		"industry": "retail",
		"size":     "1-10",
		"currency": "GHS",
		"customer": map[string]any{"name": "ABC Stores", "email": "abc@complete.test"},
		"debt":     map[string]any{"amount": 250000, "dueDate": "2027-12-01"},
	}

	st, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/onboarding/complete", token, body)
	if st != http.StatusCreated {
		t.Fatalf("status %d, %s", st, raw)
	}

	var out struct {
		Business   businessBody `json:"business"`
		CustomerID *string      `json:"customerId"`
		DebtID     *string      `json:"debtId"`
	}
	decode(t, env.Data, &out)

	if out.CustomerID == nil || out.DebtID == nil {
		t.Fatalf("customerId/debtId missing: %+v", out)
	}
	if out.Business.Currency != "GHS" {
		t.Errorf("currency = %q, want GHS", out.Business.Currency)
	}
	if !out.Business.OnboardingCompleted {
		t.Error("onboardingCompleted = false right after completing")
	}
	// The debt it just created locks the currency.
	if !out.Business.CurrencyLocked {
		t.Error("currencyLocked = false despite the debt just created")
	}

	t.Run("the records really exist", func(t *testing.T) {
		if st, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/customers/"+*out.CustomerID, token, nil); st != http.StatusOK {
			t.Errorf("customer fetch: %d", st)
		}
		if st, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/debts/"+*out.DebtID, token, nil); st != http.StatusOK {
			t.Errorf("debt fetch: %d", st)
		}
	})

	// A double submit must not create a second "first" customer.
	t.Run("completing twice is rejected", func(t *testing.T) {
		if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/onboarding/complete", token, body); st != http.StatusConflict {
			t.Errorf("second complete returned %d, want 409", st)
		}
	})
}

func TestOnboarding_completeRollsBackOnInvalidDebt(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "b@rollback.test")

	st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/onboarding/complete", token, map[string]any{
		"industry": "retail",
		"customer": map[string]any{"name": "Should Not Persist", "email": "ghost@rollback.test"},
		"debt":     map[string]any{"amount": 1000, "dueDate": "not-a-date"},
	})
	if st != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", st)
	}

	// Nothing may survive a failed transaction.
	_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/customers", token, nil)
	var list []any
	decode(t, env.Data, &list)
	if len(list) != 0 {
		t.Fatalf("got %d customers after a rolled-back complete, want 0", len(list))
	}

	if b := getBusiness(t, baseURL, token); b.OnboardingCompleted {
		t.Error("onboarding marked complete despite the failure")
	}
}

func TestOnboarding_completeAcceptsProfileOnly(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "b@profileonly.test")

	st, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/onboarding/complete", token,
		map[string]any{"industry": "services", "size": "11-50"})
	if st != http.StatusCreated {
		t.Fatalf("status %d, %s", st, raw)
	}

	var out struct {
		CustomerID *string `json:"customerId"`
		DebtID     *string `json:"debtId"`
	}
	decode(t, env.Data, &out)

	// Skipping the optional steps must still complete onboarding.
	if out.CustomerID != nil || out.DebtID != nil {
		t.Errorf("created records that were not requested: %+v", out)
	}
	if b := getBusiness(t, baseURL, token); !b.OnboardingCompleted {
		t.Error("onboarding not marked complete")
	}
}

func TestOnboarding_debtWithoutCustomerRejected(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "b@nodebt.test")

	st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/onboarding/complete", token, map[string]any{
		"industry": "retail",
		"debt":     map[string]any{"amount": 1000, "dueDate": "2027-12-01"},
	})
	if st != http.StatusBadRequest {
		t.Errorf("status %d, want 400 — a debt needs somebody to owe it", st)
	}
}

// The login response must carry enough to route without a second call.
func TestAuth_businessPayloadCarriesOnboardingState(t *testing.T) {
	baseURL, _ := newTestServer(t)

	st, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/auth/register", "", map[string]string{
		"businessName": "Routing Co",
		"email":        "routing@onboard.test",
		"password":     "longenoughpw",
		"name":         "Tester",
	})
	if st != http.StatusCreated {
		t.Fatalf("register: %d, %s", st, raw)
	}

	var out struct {
		Business struct {
			Currency            string `json:"currency"`
			OnboardingCompleted bool   `json:"onboardingCompleted"`
		} `json:"business"`
	}
	decode(t, env.Data, &out)

	if out.Business.Currency != "NGN" {
		t.Errorf("currency = %q, want NGN — the frontend needs it to render amounts", out.Business.Currency)
	}
	if out.Business.OnboardingCompleted {
		t.Error("onboardingCompleted = true for a fresh registration")
	}
}

func TestBusiness_tenantIsolation(t *testing.T) {
	baseURL, _ := newTestServer(t)

	tokenA := registerAndLogin(t, baseURL, "a@bizio.test")
	tokenB := registerAndLogin(t, baseURL, "b@bizio.test")

	doJSON(t, http.MethodPatch, baseURL+"/api/businesses/current", tokenA,
		map[string]any{"industry": "tenant-a-only", "currency": "KES"})

	b := getBusiness(t, baseURL, tokenB)
	if b.Industry != nil && *b.Industry == "tenant-a-only" {
		t.Fatal("tenant B sees tenant A's profile")
	}
	if b.Currency != "NGN" {
		t.Errorf("tenant B currency = %q, want its own NGN default", b.Currency)
	}
}

func TestBusiness_requiresAuth(t *testing.T) {
	baseURL, _ := newTestServer(t)

	for _, p := range []string{"/api/businesses/current", "/api/onboarding/status"} {
		if st, _, _ := doJSON(t, http.MethodGet, baseURL+p, "", nil); st != http.StatusUnauthorized {
			t.Errorf("%s returned %d without a token, want 401", p, st)
		}
	}
	if st, _, _ := doJSON(t, http.MethodPost, baseURL+"/api/onboarding/complete", "", map[string]any{}); st != http.StatusUnauthorized {
		t.Errorf("complete returned %d without a token, want 401", st)
	}
}
