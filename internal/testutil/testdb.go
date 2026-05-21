// Package testutil holds test-only helpers. Importing it from non-test code
// would be a smell; keep it confined to *_test.go files.
package testutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Justdan111/credflow-api/pkg/database"
)

const defaultTestDatabaseURL = "postgres://credflow:credflow_dev@localhost:5432/credflow_test?sslmode=disable"

// NewTestDB returns a pgxpool connected to the test database, with migrations
// applied. The pool is closed automatically when the test ends.
//
// If the test database is unreachable, the test is skipped with a clear
// message — so unit-only runs (no Docker) don't fail mysteriously.
func NewTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = defaultTestDatabaseURL
	}

	migrationsPath := findMigrationsPath(t)
	if err := database.RunMigrations(migrationsPath, dbURL); err != nil {
		t.Skipf("test database unreachable (%v) — start Docker + create credflow_test db, or set TEST_DATABASE_URL", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, database.Config{
		URL:            dbURL,
		MaxConns:       5,
		MinConns:       1,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Truncate wipes data from the named tables, resetting any sequences. Tests
// call this in setup so each test starts from a known empty state.
// We use TRUNCATE ... CASCADE so foreign-key dependents are cleared too.
func Truncate(t *testing.T, pool *pgxpool.Pool, tables ...string) {
	t.Helper()
	if len(tables) == 0 {
		return
	}
	q := "TRUNCATE TABLE "
	for i, table := range tables {
		if i > 0 {
			q += ", "
		}
		q += table
	}
	q += " RESTART IDENTITY CASCADE"
	if _, err := pool.Exec(context.Background(), q); err != nil {
		t.Fatalf("truncate %v: %v", tables, err)
	}
}

// findMigrationsPath walks up from the current working directory looking
// for a go.mod file (the project root), then returns root/migrations.
// Lets tests in any subdirectory find the migrations folder without
// hard-coded relative paths.
func findMigrationsPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "migrations")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find project root (go.mod) walking up from %s", dir)
		}
		dir = parent
	}
}
