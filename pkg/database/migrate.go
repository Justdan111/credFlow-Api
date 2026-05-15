package database

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"

	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// RunMigrations applies any pending up-migrations from migrationsPath
// against dbURL. Safe to call from multiple processes concurrently —
// golang-migrate uses a Postgres advisory lock internally.
func RunMigrations(migrationsPath, dbURL string) error {
	pgxURL, err := toPgx5URL(dbURL)
	if err != nil {
		return err
	}

	m, err := migrate.New("file://"+migrationsPath, pgxURL)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if srcErr != nil {
			fmt.Printf("warning: migrator source close: %v\n", srcErr)
		}
		if dbErr != nil {
			fmt.Printf("warning: migrator db close: %v\n", dbErr)
		}
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func toPgx5URL(dbURL string) (string, error) {
	const want = "postgres://"
	if !strings.HasPrefix(dbURL, want) {
		return "", fmt.Errorf("expected postgres:// url, got %q", dbURL)
	}
	return "pgx5://" + strings.TrimPrefix(dbURL, want), nil
}
