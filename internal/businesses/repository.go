package businesses

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("business not found")

// DBTX matches the pattern used across the other packages: the same methods
// work whether or not the caller is inside a transaction.
type DBTX interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Pool() *pgxpool.Pool { return r.db }

const businessSelect = `
	id, name, industry, size, currency, monthly_collection_target,
	onboarding_completed_at, created_at, updated_at`

func scanBusiness(row pgx.Row) (Business, error) {
	var b Business
	err := row.Scan(&b.ID, &b.Name, &b.Industry, &b.Size, &b.Currency,
		&b.MonthlyCollectionTarget, &b.OnboardingCompletedAt, &b.CreatedAt, &b.UpdatedAt)
	b.OnboardingCompleted = b.OnboardingCompletedAt != nil
	return b, err
}

func (r *Repository) Get(ctx context.Context, db DBTX, businessID string) (Business, error) {
	q := `SELECT ` + businessSelect + ` FROM businesses WHERE id = $1`
	b, err := scanBusiness(db.QueryRow(ctx, q, businessID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Business{}, ErrNotFound
	}
	return b, err
}

// HasFinancialRecords reports whether any debt or payment exists. It is the
// currency lock: once real amounts are stored, changing the currency would
// reinterpret every one of them without converting anything.
//
// Run inside the update transaction so it cannot race a debt being created
// concurrently.
func (r *Repository) HasFinancialRecords(ctx context.Context, db DBTX, businessID string) (bool, error) {
	const q = `
		SELECT EXISTS (SELECT 1 FROM debts    WHERE business_id = $1 AND deleted_at IS NULL)
		    OR EXISTS (SELECT 1 FROM payments WHERE business_id = $1 AND deleted_at IS NULL)`
	var exists bool
	err := db.QueryRow(ctx, q, businessID).Scan(&exists)
	return exists, err
}

// Update applies a partial change. Only the fields present in the request are
// touched; COALESCE would be wrong here because it cannot distinguish "omitted"
// from "explicitly set to null", which the target field needs.
func (r *Repository) Update(ctx context.Context, db DBTX, businessID string, req UpdateRequest) (Business, error) {
	set := make([]string, 0, 5)
	args := []any{businessID}
	add := func(col string, val any) {
		args = append(args, val)
		set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
	}

	if req.Name != nil {
		add("name", *req.Name)
	}
	if req.Industry != nil {
		add("industry", nullIfEmpty(*req.Industry))
	}
	if req.Size != nil {
		add("size", nullIfEmpty(*req.Size))
	}
	if req.Currency != nil {
		add("currency", *req.Currency)
	}
	if req.MonthlyCollectionTarget != nil {
		add("monthly_collection_target", *req.MonthlyCollectionTarget)
	}

	if len(set) == 0 {
		// Nothing to change: return the current row rather than issue a no-op
		// UPDATE that would still bump updated_at.
		return r.Get(ctx, db, businessID)
	}

	q := `UPDATE businesses SET ` + strings.Join(set, ", ") +
		` WHERE id = $1 RETURNING ` + businessSelect
	b, err := scanBusiness(db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Business{}, ErrNotFound
	}
	return b, err
}

// ClearTarget sets the collection target back to null. Separate from Update
// because a nil pointer there means "leave alone", not "clear".
func (r *Repository) ClearTarget(ctx context.Context, db DBTX, businessID string) (Business, error) {
	const q = `UPDATE businesses SET monthly_collection_target = NULL
	           WHERE id = $1 RETURNING ` + businessSelect
	return scanBusiness(db.QueryRow(ctx, q, businessID))
}

// OnboardingProgress reports what the business actually has, so the status is
// derived from records rather than a stored counter that could drift.
func (r *Repository) OnboardingProgress(ctx context.Context, db DBTX, businessID string) (hasProfile, hasCustomer, hasDebt bool, err error) {
	const q = `
		SELECT
			(SELECT industry IS NOT NULL AND industry <> '' FROM businesses WHERE id = $1),
			EXISTS (SELECT 1 FROM customers WHERE business_id = $1 AND deleted_at IS NULL),
			EXISTS (SELECT 1 FROM debts     WHERE business_id = $1 AND deleted_at IS NULL)`
	err = db.QueryRow(ctx, q, businessID).Scan(&hasProfile, &hasCustomer, &hasDebt)
	return
}

func (r *Repository) MarkOnboardingComplete(ctx context.Context, db DBTX, businessID string) error {
	const q = `UPDATE businesses
	           SET onboarding_completed_at = NOW(), onboarding_step = NULL
	           WHERE id = $1`
	_, err := db.Exec(ctx, q, businessID)
	return err
}

func (r *Repository) SetOnboardingStep(ctx context.Context, db DBTX, businessID, step string) error {
	const q = `UPDATE businesses SET onboarding_step = $2 WHERE id = $1`
	_, err := db.Exec(ctx, q, businessID, nullIfEmpty(step))
	return err
}

func (r *Repository) OnboardingCompletedAt(ctx context.Context, db DBTX, businessID string) (*time.Time, error) {
	const q = `SELECT onboarding_completed_at FROM businesses WHERE id = $1`
	var at *time.Time
	err := db.QueryRow(ctx, q, businessID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return at, err
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
