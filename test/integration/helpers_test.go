//go:build integration

// Shared helpers for the integration test suite. The `integration` build tag
// keeps these out of `go test ./...` (the fast unit path) — opt in with
// `go test -tags=integration ./test/integration/...` or `make test-integration`.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Justdan111/credflow-api/internal/analytics"
	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/internal/businesses"
	"github.com/Justdan111/credflow-api/internal/customers"
	"github.com/Justdan111/credflow-api/internal/debts"
	appmiddleware "github.com/Justdan111/credflow-api/internal/middleware"
	"github.com/Justdan111/credflow-api/internal/payments"
	"github.com/Justdan111/credflow-api/internal/testutil"
	"github.com/Justdan111/credflow-api/pkg/ratelimit"
)

const (
	testRefreshTTL  = 30 * 24 * time.Hour
	testAbsoluteTTL = 90 * 24 * time.Hour
)

var testAllowedOrigins = []string{"http://localhost:5173"}

// newTestServer wires the same stack main.go does, against the test database,
// and returns the URL it's listening on. The server is closed automatically
// when the test ends.
func newTestServer(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewTestDB(t)
	testutil.Truncate(t, pool, "password_reset_tokens", "customer_risk_snapshots", "refresh_tokens", "payments", "debts", "customers", "users", "businesses")

	jwtSvc := auth.NewJWTService("integration-test-secret", time.Hour)

	testMailer = &recordingMailer{}
	limiter := ratelimit.NewMemory()
	authSvc := auth.NewService(pool, auth.NewRepository(), jwtSvc, testRefreshTTL, testAbsoluteTTL,
		testMailer, "http://localhost:5173", time.Hour)
	// Secure:false — httptest serves plain http, so a Secure cookie would
	// never be stored by the client.
	authHandler := auth.NewHandler(authSvc, auth.CookieConfig{Secure: false}, testRefreshTTL)

	customerSvc := customers.NewService(customers.NewRepository(pool))
	debtSvcForBiz := debts.NewService(debts.NewRepository(pool))
	businessHandler := businesses.NewHandler(businesses.NewService(pool,
		businesses.NewRepository(pool), customerSvc, debtSvcForBiz))

	analyticsSvc := analytics.NewService(analytics.NewRepository(pool))
	analyticsHandler := analytics.NewHandler(analyticsSvc)

	customerHandler := customers.NewHandler(customers.NewService(customers.NewRepository(pool)))
	debtRepo := debts.NewRepository(pool)
	debtHandler := debts.NewHandler(debts.NewService(debtRepo))
	paymentHandler := payments.NewHandler(payments.NewService(payments.NewRepository(pool)), debtRepo)

	r := chi.NewRouter()
	r.Use(appmiddleware.SecurityHeaders(true))
	r.Use(appmiddleware.BodyLimit(testMaxBodyBytes))
	r.Use(chimiddleware.Recoverer)
	// chimiddleware.RealIP is deliberately NOT mounted, mirroring the default
	// TRUST_PROXY_HEADERS=false: RemoteAddr stays the true peer so a forged
	// X-Forwarded-For cannot reset a rate limit.

	originCheck := appmiddleware.RequireAllowedOrigin(testAllowedOrigins)

	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/register", authHandler.Register)
		r.With(testLimitLogin(limiter)).Post("/login", authHandler.Login)
		r.With(originCheck).Post("/refresh", authHandler.Refresh)
		r.With(originCheck).Post("/logout", authHandler.Logout)
		r.With(testLimitForgot(limiter)).Post("/forgot-password", authHandler.ForgotPassword)
		r.Post("/reset-password", authHandler.ResetPassword)
		r.Group(func(r chi.Router) {
			r.Use(appmiddleware.RequireAuth(jwtSvc))
			r.Get("/me", authHandler.Me)
			r.Patch("/me", authHandler.UpdateMe)
			r.With(originCheck).Post("/change-password", authHandler.ChangePassword)
			r.Get("/sessions", authHandler.ListSessions)
			r.With(originCheck).Delete("/sessions/{sessionId}", authHandler.RevokeSession)
		})
	})
	ownerAdmin := appmiddleware.RequireRole(auth.RoleOwner, auth.RoleAdmin)
	ownerOnly := appmiddleware.RequireRole(auth.RoleOwner)

	r.Route("/api/customers", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(jwtSvc))
		r.Get("/", customerHandler.List)
		r.Post("/", customerHandler.Create)
		r.Get("/{customerId}", customerHandler.Get)
		r.Patch("/{customerId}", customerHandler.Update)
		r.With(ownerAdmin).Delete("/{customerId}", customerHandler.Delete)
		r.Get("/{customerId}/debts", debtHandler.ListByCustomer)
		r.Get("/{customerId}/payments", paymentHandler.ListByCustomer)
	})
	r.Route("/api/debts", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(jwtSvc))
		r.Get("/", debtHandler.List)
		r.Post("/", debtHandler.Create)
		r.Get("/{debtId}", debtHandler.Get)
		r.With(ownerAdmin).Patch("/{debtId}", debtHandler.Update)
		r.With(ownerAdmin).Delete("/{debtId}", debtHandler.Delete)
		r.Post("/{debtId}/mark-paid", debtHandler.MarkPaid)
		r.Post("/{debtId}/payments", paymentHandler.CreateForDebt)
	})
	r.Route("/api/businesses", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(jwtSvc))
		r.Get("/current", businessHandler.Get)
		r.With(ownerAdmin).Patch("/current", businessHandler.Update)
	})
	r.Route("/api/onboarding", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(jwtSvc))
		r.Get("/status", businessHandler.OnboardingStatus)
		r.Post("/complete", businessHandler.OnboardingComplete)
	})

	r.Route("/api/dashboard", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(jwtSvc))
		r.Get("/summary", analyticsHandler.Summary)
		r.Get("/recent-debts", analyticsHandler.RecentDebts)
		r.Get("/recent-payments", analyticsHandler.RecentPayments)
		r.Get("/risk-distribution", analyticsHandler.RiskDistribution)
		r.Get("/collections-trend", analyticsHandler.CollectionsTrend)
	})
	r.Route("/api/analytics", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(jwtSvc))
		r.Get("/collection-rate", analyticsHandler.CollectionRate)
		r.Get("/risk-trend", analyticsHandler.RiskTrend)
		r.Get("/customer-segments", analyticsHandler.CustomerSegments)
		r.Get("/export", analyticsHandler.Export)
	})

	r.Route("/api/payments", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(jwtSvc))
		r.Get("/", paymentHandler.List)
		r.Post("/", paymentHandler.Create)
		r.Get("/{paymentId}", paymentHandler.Get)
		r.With(ownerOnly).Delete("/{paymentId}", paymentHandler.Delete)
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv.URL, pool
}

// envelope mirrors pkg/response.Response with json.RawMessage so individual
// tests can decode the data block into whatever shape they expect.
type envelope struct {
	Data  json.RawMessage `json:"data"`
	Meta  json.RawMessage `json:"meta"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// doJSON sends a JSON request and returns the status, parsed envelope, and
// raw body. It is a *test* helper, so it fails the test on transport errors.
func doJSON(t *testing.T, method, url, token string, body any) (int, envelope, []byte) {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var env envelope
	if len(raw) > 0 && !strings.HasPrefix(strings.TrimSpace(string(raw)), "<") {
		_ = json.Unmarshal(raw, &env)
	}
	return resp.StatusCode, env, raw
}

// registerAndLogin creates a fresh user via the API and returns their token.
// Each integration test that needs a logged-in user calls this with a unique
// email so tests can't accidentally collide on the unique index.
func registerAndLogin(t *testing.T, baseURL, email string) string {
	t.Helper()
	body := map[string]string{
		"businessName": "Tenant for " + email,
		"email":        email,
		"password":     "longenoughpw",
		"name":         "Tester",
	}
	status, env, raw := doJSON(t, http.MethodPost, baseURL+"/api/auth/register", "", body)
	if status != http.StatusCreated {
		t.Fatalf("register: status %d, body %s", status, raw)
	}
	var data struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if data.AccessToken == "" {
		t.Fatal("register: accessToken missing from response")
	}
	return data.AccessToken
}

// recordingMailer captures the reset link instead of sending it, so tests can
// follow the flow end to end without any external service.
type recordingMailer struct {
	mu   sync.Mutex
	sent []sentMail
}

type sentMail struct {
	To, Name, URL string
}

func (m *recordingMailer) SendPasswordReset(_ context.Context, to, name, url string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMail{To: to, Name: name, URL: url})
	return nil
}

func (m *recordingMailer) last() (sentMail, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return sentMail{}, false
	}
	return m.sent[len(m.sent)-1], true
}

func (m *recordingMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

// testMailer is set by newTestServer so the reset tests can read what was sent.
var testMailer *recordingMailer

// Test limits are smaller than production so a security test can reach them
// quickly, but must stay high enough not to trip unrelated tests: several
// legitimately log in three or four times for the same account. The production
// values live in cmd/server/main.go.
const (
	testMaxBodyBytes = 4096
	testLoginLimit   = 20
	testForgotLimit  = 2
)

func testLimitLogin(l ratelimit.Limiter) func(http.Handler) http.Handler {
	return appmiddleware.RateLimit(l, appmiddleware.RateLimitRule{
		Name: "login", Limit: testLoginLimit, Window: time.Minute,
		KeyFunc: appmiddleware.ByIPAndEmail,
	})
}

func testLimitForgot(l ratelimit.Limiter) func(http.Handler) http.Handler {
	return appmiddleware.RateLimit(l, appmiddleware.RateLimitRule{
		Name: "forgot-email", Limit: testForgotLimit, Window: time.Hour,
		KeyFunc: appmiddleware.ByEmailField,
	})
}
