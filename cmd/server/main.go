package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/Justdan111/credflow-api/internal/analytics"
	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/internal/businesses"
	"github.com/Justdan111/credflow-api/internal/customers"
	"github.com/Justdan111/credflow-api/internal/debts"
	appmiddleware "github.com/Justdan111/credflow-api/internal/middleware"
	"github.com/Justdan111/credflow-api/internal/payments"
	"github.com/Justdan111/credflow-api/pkg/database"
	"github.com/Justdan111/credflow-api/pkg/mailer"
	"github.com/Justdan111/credflow-api/pkg/ratelimit"
	"github.com/Justdan111/credflow-api/pkg/response"
)

type App struct {
	DB   *pgxpool.Pool
	JWT  *auth.JWTService
	Auth *auth.Service
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("no .env file loaded (%v) — falling back to process env", err)
	}

	dbURL := mustEnv("DATABASE_URL")
	jwtSecret := mustEnv("JWT_SECRET")
	// 15 minutes, not 24 hours: the whole point of refresh tokens is a short
	// window in which a stolen access token is useful.
	jwtTTL := envDuration("JWT_TTL", 15*time.Minute)
	refreshTTL := envDuration("REFRESH_TTL", 30*24*time.Hour)
	refreshAbsoluteTTL := envDuration("REFRESH_ABSOLUTE_TTL", 90*24*time.Hour)
	allowedOrigins := envCSV("ALLOWED_ORIGINS", []string{"http://localhost:5173"})
	cookieSecure := envBool("COOKIE_SECURE", true)
	appBaseURL := envString("APP_BASE_URL", "http://localhost:5173")
	// Short by design: a reset link is a bearer credential for taking over an
	// account, so it should not sit valid in an inbox for long.
	resetTTL := envDuration("PASSWORD_RESET_TTL", time.Hour)
	// Defaults to FALSE deliberately. Trusting X-Forwarded-For without a proxy
	// that overwrites it lets any client forge its own address and bypass every
	// IP-based rate limit. Enable only when a load balancer sits in front.
	trustProxyHeaders := envBool("TRUST_PROXY_HEADERS", false)
	maxBodyBytes := int64(envInt32("MAX_REQUEST_BODY_BYTES", 1<<20)) // 1 MiB
	enableHSTS := envBool("ENABLE_HSTS", true)
	maxConns := envInt32("DB_MAX_CONNS", 10)
	minConns := envInt32("DB_MIN_CONNS", 2)
	port := envString("PORT", "8080")

	log.Println("applying migrations...")
	if err := database.RunMigrations("migrations", dbURL); err != nil {
		log.Fatalf("migrations failed: %v", err)
	}
	log.Println("migrations applied")

	pool, err := database.Connect(context.Background(), database.Config{
		URL:            dbURL,
		MaxConns:       maxConns,
		MinConns:       minConns,
		ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		log.Fatalf("database connect failed: %v", err)
	}
	defer pool.Close()

	jwtSvc := auth.NewJWTService(jwtSecret, jwtTTL)
	authRepo := auth.NewRepository()
	// ConsoleMailer logs the reset link instead of sending it. Swapping in a
	// real provider is one new type satisfying the same interface.
	mail := mailer.NewConsoleMailer()
	authSvc := auth.NewService(pool, authRepo, jwtSvc, refreshTTL, refreshAbsoluteTTL,
		mail, appBaseURL, resetTTL)
	authHandler := auth.NewHandler(authSvc, auth.CookieConfig{Secure: cookieSecure}, refreshTTL)

	customerRepo := customers.NewRepository(pool)
	customerSvc := customers.NewService(customerRepo)
	customerHandler := customers.NewHandler(customerSvc)

	debtRepo := debts.NewRepository(pool)
	debtSvc := debts.NewService(debtRepo)
	debtHandler := debts.NewHandler(debtSvc)

	businessSvc := businesses.NewService(pool, businesses.NewRepository(pool),
		customerSvc, debtSvc)
	businessHandler := businesses.NewHandler(businessSvc)

	analyticsSvc := analytics.NewService(analytics.NewRepository(pool))
	analyticsHandler := analytics.NewHandler(analyticsSvc)

	paymentRepo := payments.NewRepository(pool)
	paymentSvc := payments.NewService(paymentRepo)
	paymentHandler := payments.NewHandler(paymentSvc, debtRepo)

	app := &App{DB: pool, JWT: jwtSvc, Auth: authSvc}

	limiter := ratelimit.NewMemory()

	r := chi.NewRouter()
	// CORS must run before anything that can short-circuit, so even error
	// responses carry the headers a browser needs to read them.
	r.Use(appmiddleware.CORS(allowedOrigins))
	r.Use(appmiddleware.SecurityHeaders(enableHSTS))
	r.Use(appmiddleware.BodyLimit(maxBodyBytes))
	r.Use(chimiddleware.RequestID)
	// RealIP rewrites RemoteAddr from client-supplied headers with no
	// validation, so it is mounted ONLY when something upstream is known to
	// overwrite them. Otherwise RemoteAddr stays the true TCP peer and the rate
	// limiter cannot be fooled by a forged X-Forwarded-For.
	if trustProxyHeaders {
		r.Use(chimiddleware.RealIP)
	}
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(60 * time.Second))

	r.Get("/health", app.handleHealth)
	r.Get("/health/db", app.handleHealthDB)

	// Defence in depth behind SameSite=Strict on the state-changing token
	// endpoints. Reads are not guarded: they carry no cookie-backed authority.
	originCheck := appmiddleware.RequireAllowedOrigin(allowedOrigins)

	// Rate limits. Tight buckets on the composite or account key, loose
	// ceilings on IP: carrier-grade NAT is widespread on African mobile
	// networks, so many unrelated users share one address and an IP-only limit
	// would lock out an entire operator pool.
	limitLogin := appmiddleware.RateLimit(limiter, appmiddleware.RateLimitRule{
		Name: "login", Limit: 5, Window: time.Minute,
		// IP+email: two subscribers behind one NAT get separate buckets.
		KeyFunc: appmiddleware.ByIPAndEmail,
	})
	// Register is the one endpoint with no pre-existing account to key on, so
	// it can only use IP — the very key the login limit avoids relying on. On
	// carrier-grade NAT a whole office or ISP pool shares one address, so a
	// tight limit here would block legitimate signups exactly where this phase
	// is trying to be careful. 10/hour still stops bulk automation cold while
	// leaving room for a real shared connection.
	limitRegister := appmiddleware.RateLimit(limiter, appmiddleware.RateLimitRule{
		Name: "register", Limit: 10, Window: time.Hour, KeyFunc: appmiddleware.ByIP,
	})
	limitForgot := appmiddleware.RateLimit(limiter,
		// Per-address, so one person cannot be mail-bombed...
		appmiddleware.RateLimitRule{
			Name: "forgot-email", Limit: 3, Window: time.Hour,
			KeyFunc: appmiddleware.ByEmailField,
		},
		// ...plus a loose per-IP ceiling against bulk abuse across addresses.
		appmiddleware.RateLimitRule{
			Name: "forgot-ip", Limit: 20, Window: time.Hour, KeyFunc: appmiddleware.ByIP,
		},
	)
	limitReset := appmiddleware.RateLimit(limiter, appmiddleware.RateLimitRule{
		Name: "reset", Limit: 10, Window: time.Hour, KeyFunc: appmiddleware.ByIP,
	})
	limitRefresh := appmiddleware.RateLimit(limiter, appmiddleware.RateLimitRule{
		Name: "refresh", Limit: 30, Window: time.Minute, KeyFunc: appmiddleware.ByIP,
	})

	r.Route("/api/auth", func(r chi.Router) {
		r.With(limitRegister).Post("/register", authHandler.Register)
		r.With(limitLogin).Post("/login", authHandler.Login)

		// Deliberately outside RequireAuth: refresh must work precisely when
		// the access token has expired. The httpOnly cookie is the credential.
		r.With(originCheck, limitRefresh).Post("/refresh", authHandler.Refresh)
		r.With(originCheck).Post("/logout", authHandler.Logout)

		// Unauthenticated by necessity: a locked-out user has no token.
		r.With(limitForgot).Post("/forgot-password", authHandler.ForgotPassword)
		r.With(limitReset).Post("/reset-password", authHandler.ResetPassword)

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

	// Dashboard and analytics read the same tables and share one package; the
	// split is only a URL grouping the frontend expects.
	r.Route("/api/businesses", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(jwtSvc))
		r.Get("/current", businessHandler.Get)
		// Editing the profile is administrative: currency and the collection
		// target shape every financial figure the business reports.
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

	addr := ":" + port
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// appCtx is cancelled on shutdown so background workers stop with the
	// server rather than being killed mid-query.
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	go runTokenCleanup(appCtx, authSvc)
	go runRiskSnapshots(appCtx, analyticsSvc)
	go runLimiterSweep(appCtx, limiter)

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("CredFlow API listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Fatalf("server failed to start: %v", err)
	case sig := <-stop:
		log.Printf("received signal %s — shutting down server...", sig)
	}

	cancelApp() // stop background workers before draining connections

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("graceful shutdown failed: %v", err)
	}
	log.Println("server stopped cleanly")
}

// runTokenCleanup purges long-expired refresh tokens once a day. Rows are kept
// for a grace period past expiry so reuse detection can still recognise a
// replayed token instead of dismissing it as unknown.
func runTokenCleanup(ctx context.Context, svc *auth.Service) {
	const (
		interval = 24 * time.Hour
		grace    = 30 * 24 * time.Hour
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Bound each sweep so a slow delete cannot outlive the interval.
			sweepCtx, cancel := context.WithTimeout(ctx, time.Minute)
			n, err := svc.CleanupExpiredTokens(sweepCtx, grace)
			if err != nil {
				log.Printf("refresh token cleanup failed: %v", err)
			} else if n > 0 {
				log.Printf("refresh token cleanup removed %d expired rows", n)
			}
			// Reset tokens are short-lived, so a much shorter grace is enough.
			rn, rerr := svc.CleanupExpiredResetTokens(sweepCtx, 24*time.Hour)
			cancel()
			if rerr != nil {
				log.Printf("reset token cleanup failed: %v", rerr)
			} else if rn > 0 {
				log.Printf("reset token cleanup removed %d expired rows", rn)
			}
		}
	}
}

// runRiskSnapshots records the customer risk distribution once a day.
//
// customers.risk_level is mutable with no history, so a past month's mix is
// unrecoverable unless it is written down as it happens. The snapshot runs once
// at startup too, so a fresh deployment has a data point immediately rather
// than an empty chart for 24 hours.
func runRiskSnapshots(ctx context.Context, svc *analytics.Service) {
	const interval = 24 * time.Hour

	write := func() {
		// Bound each run so a slow write cannot outlive the interval.
		runCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		n, err := svc.WriteRiskSnapshots(runCtx)
		if err != nil {
			log.Printf("risk snapshot failed: %v", err)
			return
		}
		log.Printf("risk snapshot recorded for %d businesses", n)
	}

	write()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			write()
		}
	}
}

// runLimiterSweep evicts idle rate-limit buckets so memory does not grow with
// every address that has ever connected.
func runLimiterSweep(ctx context.Context, limiter *ratelimit.Memory) {
	const (
		interval = 5 * time.Minute
		// Comfortably longer than the widest window (1 hour), so a bucket is
		// never dropped while it still constrains anybody.
		idleFor = 2 * time.Hour
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := limiter.Sweep(idleFor); n > 0 {
				log.Printf("rate limiter swept %d idle buckets", n)
			}
		}
	}
}

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	response.Success(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) handleHealthDB(w http.ResponseWriter, r *http.Request) {
	pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := a.DB.Ping(pingCtx); err != nil {
		response.Fail(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	response.Success(w, http.StatusOK, map[string]string{"status": "ok", "db": "ok"})
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envCSV reads a comma-separated list, e.g. ALLOWED_ORIGINS.
func envCSV(key string, fallback []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatalf("env %s: invalid bool %q: %v", key, v, err)
	}
	return b
}

func envInt32(key string, fallback int32) int32 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		log.Fatalf("env %s: invalid int32 %q: %v", key, v, err)
	}
	return int32(n)
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("env %s: invalid duration %q: %v", key, v, err)
	}
	return d
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("env %s is required", key)
	}
	return v
}
