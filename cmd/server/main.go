package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/internal/customers"
	appmiddleware "github.com/Justdan111/credflow-api/internal/middleware"
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
	jwtTTL := envDuration("JWT_TTL", 24*time.Hour)
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
	authSvc := auth.NewService(pool, authRepo, jwtSvc)
	authHandler := auth.NewHandler(authSvc)

	customerRepo := customers.NewRepository(pool)
	customerSvc := customers.NewService(customerRepo)
	customerHandler := customers.NewHandler(customerSvc)

	app := &App{DB: pool, JWT: jwtSvc, Auth: authSvc}

	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(60 * time.Second))

	r.Get("/health", app.handleHealth)
	r.Get("/health/db", app.handleHealthDB)

	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/register", authHandler.Register)
		r.Post("/login", authHandler.Login)

		r.Group(func(r chi.Router) {
			r.Use(appmiddleware.RequireAuth(jwtSvc))
			r.Get("/me", authHandler.Me)
		})
	})

	r.Route("/api/customers", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(jwtSvc))
		r.Get("/", customerHandler.List)
		r.Post("/", customerHandler.Create)
		r.Get("/{customerId}", customerHandler.Get)
		r.Patch("/{customerId}", customerHandler.Update)
		r.Delete("/{customerId}", customerHandler.Delete)
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("graceful shutdown failed: %v", err)
	}
	log.Println("server stopped cleanly")
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
