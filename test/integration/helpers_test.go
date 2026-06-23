//go:build integration

// Shared helpers for the integration test suite. The `integration` build tag
// keeps these out of `go test ./...` (the fast unit path) — opt in with
// `go test -tags=integration ./test/integration/...` or `make test-integration`.
package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/internal/customers"
	"github.com/Justdan111/credflow-api/internal/debts"
	appmiddleware "github.com/Justdan111/credflow-api/internal/middleware"
	"github.com/Justdan111/credflow-api/internal/payments"
	"github.com/Justdan111/credflow-api/internal/testutil"
)

// newTestServer wires the same stack main.go does, against the test database,
// and returns the URL it's listening on. The server is closed automatically
// when the test ends.
func newTestServer(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewTestDB(t)
	testutil.Truncate(t, pool, "payments", "debts", "customers", "users", "businesses")

	jwtSvc := auth.NewJWTService("integration-test-secret", time.Hour)

	authSvc := auth.NewService(pool, auth.NewRepository(), jwtSvc)
	authHandler := auth.NewHandler(authSvc)

	customerHandler := customers.NewHandler(customers.NewService(customers.NewRepository(pool)))
	debtRepo := debts.NewRepository(pool)
	debtHandler := debts.NewHandler(debts.NewService(debtRepo))
	paymentHandler := payments.NewHandler(payments.NewService(payments.NewRepository(pool)), debtRepo)

	r := chi.NewRouter()
	r.Use(chimiddleware.Recoverer)

	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/register", authHandler.Register)
		r.Post("/login", authHandler.Login)
		r.Group(func(r chi.Router) {
			r.Use(appmiddleware.RequireAuth(jwtSvc))
			r.Get("/me", authHandler.Me)
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
		Token string `json:"token"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if data.Token == "" {
		t.Fatal("register: token missing from response")
	}
	return data.Token
}
