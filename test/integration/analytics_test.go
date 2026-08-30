//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Justdan111/credflow-api/internal/analytics"
)

// snapshotWriter builds an analytics service over the test's own pool, so the
// risk-trend test can run the daily job on demand instead of waiting for a
// 24-hour ticker. Built per test rather than shared, so one test's server
// closing its pool cannot break another's.
func snapshotWriter(pool *pgxpool.Pool) *analytics.Service {
	return analytics.NewService(analytics.NewRepository(pool))
}

// decode unmarshals the envelope's data block or fails the test.
func decode(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode: %v (raw: %s)", err, raw)
	}
}

// seedBusiness creates a customer plus a debt, and optionally pays some of it.
// Returns the customer and debt ids.
func seedDebt(t *testing.T, baseURL, token string, amount float64, dueDate, email string) (string, string) {
	t.Helper()
	customerID := createCustomer(t, baseURL, token, "Cust "+email, email)
	debtID := createDebt(t, baseURL, token, customerID, amount, dueDate)
	return customerID, debtID
}

// seedOverdueDebt creates a debt that is genuinely in the past. Both dates must
// be backdated: the schema enforces CHECK (due_date >= issued_date), so a past
// dueDate with today's default issuedDate is correctly rejected.
func seedOverdueDebt(t *testing.T, baseURL, token string, amount float64, email string) string {
	t.Helper()
	customerID := createCustomer(t, baseURL, token, "Cust "+email, email)

	st, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/debts", token, map[string]any{
		"customerId": customerID,
		"amount":     amount,
		"issuedDate": "2019-01-01",
		"dueDate":    "2020-01-01",
	})
	if st != http.StatusCreated {
		t.Fatalf("create overdue debt: %d, %s", st, raw)
	}
	var d struct {
		ID string `json:"id"`
	}
	decode(t, env.Data, &d)
	return d.ID
}

func TestDashboard_summaryOnEmptyBusiness(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "empty@dash.test")

	st, env, raw := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/summary", token, nil)
	if st != http.StatusOK {
		t.Fatalf("status %d, body %s", st, raw)
	}

	var s struct {
		Currency    string `json:"currency"`
		Outstanding struct {
			Value  float64  `json:"value"`
			Change *float64 `json:"change"`
		} `json:"outstanding"`
		Customers struct {
			Value float64 `json:"value"`
		} `json:"customers"`
	}
	decode(t, env.Data, &s)

	// An empty business must return zeros, never an error.
	if s.Currency != "NGN" {
		t.Errorf("currency = %q, want NGN (the default)", s.Currency)
	}
	if s.Outstanding.Value != 0 {
		t.Errorf("outstanding = %v, want 0", s.Outstanding.Value)
	}
	if s.Customers.Value != 0 {
		t.Errorf("customers = %v, want 0", s.Customers.Value)
	}
	// No prior period, so no percentage change.
	if s.Outstanding.Change != nil {
		t.Errorf("change = %v, want nil for a business with no history", *s.Outstanding.Change)
	}
}

func TestDashboard_summaryReflectsRealData(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "real@dash.test")

	customerID, debtID := seedDebt(t, baseURL, token, 1000, "2027-12-31", "c1@dash.test")
	recordPayment(t, baseURL, token, customerID, debtID, 250, "")
	seedDebt(t, baseURL, token, 500, "2027-12-31", "c2@dash.test")

	st, env, raw := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/summary", token, nil)
	if st != http.StatusOK {
		t.Fatalf("status %d, body %s", st, raw)
	}

	var s struct {
		Outstanding struct {
			Value float64 `json:"value"`
		} `json:"outstanding"`
		Customers struct {
			Value        float64 `json:"value"`
			NewThisMonth int     `json:"newThisMonth"`
		} `json:"customers"`
		Collected struct {
			Value float64 `json:"value"`
		} `json:"collected"`
	}
	decode(t, env.Data, &s)

	// 1000 - 250 paid, plus an untouched 500.
	if s.Outstanding.Value != 1250 {
		t.Errorf("outstanding = %v, want 1250", s.Outstanding.Value)
	}
	if s.Customers.Value != 2 {
		t.Errorf("customers = %v, want 2", s.Customers.Value)
	}
	if s.Customers.NewThisMonth != 2 {
		t.Errorf("newThisMonth = %v, want 2", s.Customers.NewThisMonth)
	}
	if s.Collected.Value != 250 {
		t.Errorf("collected = %v, want 250", s.Collected.Value)
	}
}

// A settled debt must leave outstanding, whether it was paid off or closed
// administratively.
func TestDashboard_paidDebtLeavesOutstanding(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "paid@dash.test")

	customerID, debtID := seedDebt(t, baseURL, token, 800, "2027-12-31", "c@paid.test")
	recordPayment(t, baseURL, token, customerID, debtID, 800, "")

	_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/summary", token, nil)
	var s struct {
		Outstanding struct{ Value float64 } `json:"outstanding"`
		Collected   struct{ Value float64 } `json:"collected"`
	}
	decode(t, env.Data, &s)

	if s.Outstanding.Value != 0 {
		t.Errorf("outstanding = %v, want 0 once the debt is fully paid", s.Outstanding.Value)
	}
	if s.Collected.Value != 800 {
		t.Errorf("collected = %v, want 800", s.Collected.Value)
	}
}

func TestDashboard_recentDebtsAndPayments(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "recent@dash.test")

	customerID, debtID := seedDebt(t, baseURL, token, 300, "2027-01-15", "c@recent.test")
	recordPayment(t, baseURL, token, customerID, debtID, 100, "")

	t.Run("recent debts carry the customer name", func(t *testing.T) {
		_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/recent-debts", token, nil)
		var debts []struct {
			ID           string  `json:"id"`
			CustomerName string  `json:"customerName"`
			Amount       float64 `json:"amount"`
			DueDate      string  `json:"dueDate"`
			DaysOverdue  *int    `json:"daysOverdue"`
		}
		decode(t, env.Data, &debts)

		if len(debts) != 1 {
			t.Fatalf("got %d debts, want 1", len(debts))
		}
		if debts[0].CustomerName == "" {
			t.Error("customerName is empty — the frontend should not have to resolve it")
		}
		if debts[0].DueDate != "2027-01-15" {
			t.Errorf("dueDate = %q, want 2027-01-15", debts[0].DueDate)
		}
		if debts[0].DaysOverdue != nil {
			t.Errorf("daysOverdue = %v, want nil for a future due date", *debts[0].DaysOverdue)
		}
	})

	t.Run("recent payments", func(t *testing.T) {
		_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/recent-payments", token, nil)
		var pays []struct {
			Amount       float64 `json:"amount"`
			Method       string  `json:"method"`
			CustomerName string  `json:"customerName"`
		}
		decode(t, env.Data, &pays)

		if len(pays) != 1 || pays[0].Amount != 100 {
			t.Fatalf("got %+v, want one payment of 100", pays)
		}
		if pays[0].CustomerName == "" {
			t.Error("customerName is empty")
		}
	})

	t.Run("limit is clamped", func(t *testing.T) {
		st, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/recent-debts?limit=9999", token, nil)
		if st != http.StatusOK {
			t.Fatalf("status %d — an oversized limit must clamp, not fail", st)
		}
	})
}

// An overdue debt must report a positive daysOverdue.
func TestDashboard_overdueIsDetected(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "overdue@dash.test")

	seedOverdueDebt(t, baseURL, token, 400, "c@overdue.test")

	_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/recent-debts", token, nil)
	var debts []struct {
		DaysOverdue *int `json:"daysOverdue"`
	}
	decode(t, env.Data, &debts)

	if len(debts) != 1 || debts[0].DaysOverdue == nil || *debts[0].DaysOverdue <= 0 {
		t.Fatalf("expected a positive daysOverdue, got %+v", debts)
	}

	_, env2, _ := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/summary", token, nil)
	var s struct {
		Overdue struct {
			Value         float64 `json:"value"`
			CustomerCount int     `json:"customerCount"`
		} `json:"overdue"`
	}
	decode(t, env2.Data, &s)

	if s.Overdue.Value != 400 {
		t.Errorf("overdue value = %v, want 400", s.Overdue.Value)
	}
	if s.Overdue.CustomerCount != 1 {
		t.Errorf("overdue customerCount = %d, want 1", s.Overdue.CustomerCount)
	}
}

// All three buckets must always be present so the chart keeps a stable shape.
func TestDashboard_riskDistributionAlwaysReturnsThreeBuckets(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "risk@dash.test")
	createCustomer(t, baseURL, token, "C", "c@risk.test")

	_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/risk-distribution", token, nil)
	var buckets []struct {
		RiskLevel     string  `json:"riskLevel"`
		CustomerCount int     `json:"customerCount"`
		Percentage    float64 `json:"percentage"`
	}
	decode(t, env.Data, &buckets)

	if len(buckets) != 3 {
		t.Fatalf("got %d buckets, want 3 (low, medium, high) even when empty", len(buckets))
	}
	want := []string{"low", "medium", "high"}
	total := 0.0
	for i, b := range buckets {
		if b.RiskLevel != want[i] {
			t.Errorf("bucket %d = %q, want %q — order must be stable", i, b.RiskLevel, want[i])
		}
		total += b.Percentage
	}
	if total != 100 {
		t.Errorf("percentages sum to %v, want 100", total)
	}
}

func TestDashboard_collectionsTrendReturnsEveryMonth(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "trend@dash.test")

	customerID, debtID := seedDebt(t, baseURL, token, 1000, "2027-12-31", "c@trend.test")
	recordPayment(t, baseURL, token, customerID, debtID, 300, "")

	_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/collections-trend?months=6", token, nil)
	var tr struct {
		Currency string `json:"currency"`
		Points   []struct {
			Month       string  `json:"month"`
			Label       string  `json:"label"`
			Collections float64 `json:"collections"`
		} `json:"points"`
	}
	decode(t, env.Data, &tr)

	// Months with no payments must still appear, or the chart is distorted.
	if len(tr.Points) != 6 {
		t.Fatalf("got %d points, want 6 — empty months must not be dropped", len(tr.Points))
	}
	if tr.Currency != "NGN" {
		t.Errorf("currency = %q, want NGN", tr.Currency)
	}
	if tr.Points[len(tr.Points)-1].Collections != 300 {
		t.Errorf("current month collections = %v, want 300", tr.Points[len(tr.Points)-1].Collections)
	}
	if tr.Points[0].Label == "" {
		t.Error("label is empty — the chart x-axis needs it")
	}
}

// Without a target set, the endpoint must return null rather than invent one.
func TestAnalytics_collectionRateTargetIsNullWhenUnset(t *testing.T) {
	baseURL, pool := newTestServer(t)
	token := registerAndLogin(t, baseURL, "rate@analytics.test")

	_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/analytics/collection-rate", token, nil)
	var cr struct {
		Target *float64 `json:"target"`
		Points []struct {
			Target *float64 `json:"target"`
			Rate   *float64 `json:"rate"`
		} `json:"points"`
	}
	decode(t, env.Data, &cr)

	if cr.Target != nil {
		t.Errorf("target = %v, want nil when the business has not set one", *cr.Target)
	}
	for i, p := range cr.Points {
		if p.Rate != nil {
			t.Errorf("point %d rate = %v, want nil without a target", i, *p.Rate)
		}
	}

	// With a target set, the rate becomes computable.
	if _, err := pool.Exec(context.Background(),
		`UPDATE businesses SET monthly_collection_target = 1000`); err != nil {
		t.Fatalf("set target: %v", err)
	}

	_, env2, _ := doJSON(t, http.MethodGet, baseURL+"/api/analytics/collection-rate", token, nil)
	decode(t, env2.Data, &cr)
	if cr.Target == nil || *cr.Target != 1000 {
		t.Fatalf("target = %v, want 1000", cr.Target)
	}
	for i, p := range cr.Points {
		if p.Rate == nil {
			t.Errorf("point %d rate is nil, want a value once a target exists", i)
		}
	}
}

// The risk-trend chart is empty until the snapshot job has run, and must say so
// rather than looking silently broken.
func TestAnalytics_riskTrendReportsYoungHistory(t *testing.T) {
	baseURL, pool := newTestServer(t)
	token := registerAndLogin(t, baseURL, "risktrend@analytics.test")
	createCustomer(t, baseURL, token, "C", "c@risktrend.test")

	// The daily job may not have run since this business registered. Rather
	// than show a blank chart until tomorrow, the current month is computed
	// live so a new signup sees its position immediately.
	t.Run("before any snapshot, the current month is computed live", func(t *testing.T) {
		_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/analytics/risk-trend", token, nil)
		var rt struct {
			Points []struct {
				Month             string `json:"month"`
				Low, Medium, High int
			} `json:"points"`
			Meta struct {
				HistoryStartedAt *string `json:"historyStartedAt"`
				MonthsAvailable  int     `json:"monthsAvailable"`
			} `json:"meta"`
		}
		decode(t, env.Data, &rt)

		if len(rt.Points) != 1 {
			t.Fatalf("got %d points, want 1 live point", len(rt.Points))
		}
		if rt.Points[0].Low != 1 {
			t.Errorf("live point low = %d, want 1", rt.Points[0].Low)
		}
		// No snapshot has been written, so recorded history genuinely has not
		// started yet — the live point must not fake that.
		if rt.Meta.HistoryStartedAt != nil {
			t.Errorf("historyStartedAt = %v, want nil before any snapshot is written", *rt.Meta.HistoryStartedAt)
		}
	})

	t.Run("after the daily job runs", func(t *testing.T) {
		if _, err := snapshotWriter(pool).WriteRiskSnapshots(context.Background()); err != nil {
			t.Fatalf("write snapshots: %v", err)
		}

		_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/analytics/risk-trend", token, nil)
		var rt struct {
			Points []struct {
				Low, Medium, High int
			} `json:"points"`
			Meta struct {
				HistoryStartedAt *string `json:"historyStartedAt"`
				MonthsAvailable  int     `json:"monthsAvailable"`
			} `json:"meta"`
		}
		decode(t, env.Data, &rt)

		if rt.Meta.MonthsAvailable != 1 {
			t.Fatalf("monthsAvailable = %d, want 1", rt.Meta.MonthsAvailable)
		}
		if rt.Meta.HistoryStartedAt == nil {
			t.Error("historyStartedAt is nil after a snapshot ran")
		}
		if len(rt.Points) != 1 || rt.Points[0].Low != 1 {
			t.Errorf("points = %+v, want one point with low=1", rt.Points)
		}
	})

	t.Run("running twice the same day does not duplicate", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			if _, err := snapshotWriter(pool).WriteRiskSnapshots(context.Background()); err != nil {
				t.Fatalf("write snapshots: %v", err)
			}
		}
		_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/analytics/risk-trend", token, nil)
		var rt struct {
			Meta struct {
				MonthsAvailable int `json:"monthsAvailable"`
			} `json:"meta"`
		}
		decode(t, env.Data, &rt)
		if rt.Meta.MonthsAvailable != 1 {
			t.Fatalf("monthsAvailable = %d after 4 runs, want 1 — ON CONFLICT must dedupe", rt.Meta.MonthsAvailable)
		}
	})
}

func TestAnalytics_customerSegmentsBucketByLifetimeValue(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "seg@analytics.test")

	seedDebt(t, baseURL, token, 2_000_000, "2027-12-31", "big@seg.test") // 1M+
	seedDebt(t, baseURL, token, 600_000, "2027-12-31", "mid@seg.test")   // 500K-1M
	seedDebt(t, baseURL, token, 200_000, "2027-12-31", "small@seg.test") // 100K-500K
	seedDebt(t, baseURL, token, 50_000, "2027-12-31", "tiny@seg.test")   // <100K
	createCustomer(t, baseURL, token, "No debts", "none@seg.test")       // <100K

	_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/analytics/customer-segments", token, nil)
	var cs struct {
		Currency string `json:"currency"`
		Segments []struct {
			Label         string   `json:"label"`
			Min           float64  `json:"min"`
			Max           *float64 `json:"max"`
			CustomerCount int      `json:"customerCount"`
		} `json:"segments"`
	}
	decode(t, env.Data, &cs)

	if len(cs.Segments) != 4 {
		t.Fatalf("got %d segments, want 4", len(cs.Segments))
	}
	want := []int{1, 1, 1, 2} // the customer with no debts falls in the lowest bucket
	for i, seg := range cs.Segments {
		if seg.CustomerCount != want[i] {
			t.Errorf("segment %q count = %d, want %d", seg.Label, seg.CustomerCount, want[i])
		}
	}
	// The top bucket is unbounded; bounds are returned so the frontend need not
	// hard-code currency-specific thresholds.
	if cs.Segments[0].Max != nil {
		t.Error("top segment should have a null max")
	}
}

func TestAnalytics_exportCSV(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "export@analytics.test")

	customerID, debtID := seedDebt(t, baseURL, token, 1000, "2027-12-31", "c@export.test")
	recordPayment(t, baseURL, token, customerID, debtID, 400, "")

	t.Run("returns csv", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/analytics/export?months=3", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
		if cd := resp.Header.Get("Content-Disposition"); cd == "" {
			t.Error("Content-Disposition missing — the browser needs it to download")
		}

		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		body := string(buf[:n])
		if len(body) == 0 {
			t.Fatal("empty csv")
		}
		if body[:5] != "month" {
			t.Errorf("csv does not start with a header row: %q", body[:20])
		}
	})

	t.Run("rejects an unsupported format", func(t *testing.T) {
		st, _, _ := doJSON(t, http.MethodGet, baseURL+"/api/analytics/export?format=xlsx", token, nil)
		if st != http.StatusBadRequest {
			t.Errorf("status %d, want 400 — an unsupported format must not silently return csv", st)
		}
	})
}

// Every analytics endpoint must be tenant-scoped.
func TestAnalytics_tenantIsolation(t *testing.T) {
	baseURL, _ := newTestServer(t)

	tokenA := registerAndLogin(t, baseURL, "a@iso.test")
	tokenB := registerAndLogin(t, baseURL, "b@iso.test")

	customerID, debtID := seedDebt(t, baseURL, tokenA, 5000, "2027-12-31", "ca@iso.test")
	recordPayment(t, baseURL, tokenA, customerID, debtID, 1000, "")

	t.Run("B sees none of A's money", func(t *testing.T) {
		_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/summary", tokenB, nil)
		var s struct {
			Outstanding struct{ Value float64 } `json:"outstanding"`
			Collected   struct{ Value float64 } `json:"collected"`
			Customers   struct{ Value float64 } `json:"customers"`
		}
		decode(t, env.Data, &s)

		if s.Outstanding.Value != 0 || s.Collected.Value != 0 || s.Customers.Value != 0 {
			t.Fatalf("tenant B sees A's data: %+v", s)
		}
	})

	t.Run("B sees none of A's rows", func(t *testing.T) {
		for _, path := range []string{"/api/dashboard/recent-debts", "/api/dashboard/recent-payments"} {
			_, env, _ := doJSON(t, http.MethodGet, baseURL+path, tokenB, nil)
			var rows []any
			decode(t, env.Data, &rows)
			if len(rows) != 0 {
				t.Errorf("%s returned %d rows for the wrong tenant", path, len(rows))
			}
		}
	})

	t.Run("A still sees its own", func(t *testing.T) {
		_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/dashboard/summary", tokenA, nil)
		var s struct {
			Outstanding struct{ Value float64 } `json:"outstanding"`
		}
		decode(t, env.Data, &s)
		if s.Outstanding.Value != 4000 {
			t.Errorf("outstanding = %v, want 4000", s.Outstanding.Value)
		}
	})
}

// Every endpoint must reject an unauthenticated caller.
func TestAnalytics_requiresAuth(t *testing.T) {
	baseURL, _ := newTestServer(t)

	paths := []string{
		"/api/dashboard/summary",
		"/api/dashboard/recent-debts",
		"/api/dashboard/recent-payments",
		"/api/dashboard/risk-distribution",
		"/api/dashboard/collections-trend",
		"/api/analytics/collection-rate",
		"/api/analytics/risk-trend",
		"/api/analytics/customer-segments",
		"/api/analytics/export",
	}
	for _, p := range paths {
		if st, _, _ := doJSON(t, http.MethodGet, baseURL+p, "", nil); st != http.StatusUnauthorized {
			t.Errorf("%s returned %d without a token, want 401", p, st)
		}
	}
}

// Kept as a top-level test rather than a subtest: newTestServer truncates the
// shared test database, so calling it inside another test's subtree would wipe
// that test's seeded data out from under it.
func TestAnalytics_riskTrendEmptyBusiness(t *testing.T) {
	baseURL, _ := newTestServer(t)
	token := registerAndLogin(t, baseURL, "nocustomers@risktrend.test")

	_, env, _ := doJSON(t, http.MethodGet, baseURL+"/api/analytics/risk-trend", token, nil)
	var rt struct {
		Points []any `json:"points"`
		Meta   struct {
			MonthsAvailable int `json:"monthsAvailable"`
		} `json:"meta"`
	}
	decode(t, env.Data, &rt)

	if rt.Points == nil {
		t.Error("points is null, want an empty array so the chart can consume it")
	}
	// A business with no customers gets no live point either — a row of zeros
	// would look like recorded history that does not exist.
	if rt.Meta.MonthsAvailable != 0 {
		t.Errorf("monthsAvailable = %d, want 0 for a business with no customers", rt.Meta.MonthsAvailable)
	}
}
