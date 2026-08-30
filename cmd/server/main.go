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

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/internal/customers"
	"github.com/Justdan111/credflow-api/internal/debts"
	appmiddleware "github.com/Justdan111/credflow-api/internal/middleware"
	"github.com/Justdan111/credflow-api/internal/payments"
	"github.com/Justdan111/credflow-api/pkg/database"
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
	authSvc := auth.NewService(pool, authRepo, jwtSvc, refreshTTL, refreshAbsoluteTTL)
	authHandler := auth.NewHandler(authSvc, auth.CookieConfig{Secure: cookieSecure}, refreshTTL)

	customerRepo := customers.NewRepository(pool)
	customerSvc := customers.NewService(customerRepo)
	customerHandler := customers.NewHandler(customerSvc)

	debtRepo := debts.NewRepository(pool)
	debtSvc := debts.NewService(debtRepo)
	debtHandler := debts.NewHandler(debtSvc)

	paymentRepo := payments.NewRepository(pool)
	paymentSvc := payments.NewService(paymentRepo)
	paymentHandler := payments.NewHandler(paymentSvc, debtRepo)

	app := &App{DB: pool, JWT: jwtSvc, Auth: authSvc}

	r := chi.NewRouter()
	// CORS must run before anything that can short-circuit, so even error
	// responses carry the headers a browser needs to read them.
	r.Use(appmiddleware.CORS(allowedOrigins))
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(60 * time.Second))

	r.Get("/health", app.handleHealth)
	r.Get("/health/db", app.handleHealthDB)

	// Defence in depth behind SameSite=Strict on the state-changing token
	// endpoints. Reads are not guarded: they carry no cookie-backed authority.
	originCheck := appmiddleware.RequireAllowedOrigin(allowedOrigins)

	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/register", authHandler.Register)
		r.Post("/login", authHandler.Login)

		// Deliberately outside RequireAuth: refresh must work precisely when
		// the access token has expired. The httpOnly cookie is the credential.
		r.With(originCheck).Post("/refresh", authHandler.Refresh)
		r.With(originCheck).Post("/logout", authHandler.Logout)

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
			cancel()
			if err != nil {
				log.Printf("refresh token cleanup failed: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("refresh token cleanup removed %d expired rows", n)
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
