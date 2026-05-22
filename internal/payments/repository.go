package payments

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

var (
	ErrNotFound         = errors.New("payment not found")
	ErrCustomerNotFound = errors.New("customer not found")
	ErrDebtNotFound     = errors.New("debt not found")
)

// idempotencyConstraint is the name of the unique index created in
// migration 0004. We match against it specifically so a future column-level
// unique constraint added later can't be mistaken for an idempotency replay.
const idempotencyConstraint = "payments_business_idempotency_uniq"

// DBTX is implemented by both *pgxpool.Pool and pgx.Tx, so the repository's
// methods work inside or outside a transaction.
type DBTX interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// Pool returns the underlying pool. The service uses it to start its own
// transactions (Create + RecomputeDebtStatus must be atomic).
func (r *Repository) Pool() *pgxpool.Pool { return r.db }

const paymentSelect = `
	id, business_id, customer_id, debt_id, amount, method, reference, notes,
	paid_at, idempotency_key, created_at, updated_at
`

func scanPayment(row pgx.Row) (Payment, error) {
	var p Payment
	err := row.Scan(
		&p.ID, &p.BusinessID, &p.CustomerID, &p.DebtID, &p.Amount, &p.Method,
		&p.Reference, &p.Notes, &p.PaidAt, &p.IdempotencyKey, &p.CreatedAt, &p.UpdatedAt,
	)
	return p, err
}

// Create inserts a payment. The customer-and-debt existence check is folded
// into the INSERT so it's atomic with the write. Two failure paths the
// service needs to distinguish:
//   - the WHERE EXISTS failed (customer or debt not in tenant)        -> ErrCustomerNotFound or ErrDebtNotFound
//   - the idempotency unique constraint fired (duplicate submission)  -> the existing row
//     is returned and the bool "idempotentReplay" is true.
func (r *Repository) Create(ctx context.Context, db DBTX, businessID string, req CreateRequest, paidAt time.Time) (p Payment, idempotentReplay bool, err error) {
	// We assemble the WHERE EXISTS clause based on whether debt_id is set.
	// Without a debt the requirement is just "customer in this tenant and active".
	// With a debt we also require the debt to be in the tenant, active, and
	// belong to that customer (defense-in-depth — service already checks this).
	var existsClause string
	args := []any{
		businessID,
		req.CustomerID,
		req.Amount,
		methodOrDefault(req.Method),
		nullIfEmpty(req.Reference),
		nullIfEmpty(req.Notes),
		paidAt,
		nullIfEmpty(req.IdempotencyKey),
	}
	if req.DebtID == "" {
		existsClause = `EXISTS (
			SELECT 1 FROM customers
			WHERE id = $2 AND business_id = $1 AND deleted_at IS NULL
		)`
		// debt_id stays NULL via the explicit cast below.
	} else {
		args = append(args, req.DebtID)
		existsClause = `EXISTS (
			SELECT 1 FROM customers
			WHERE id = $2 AND business_id = $1 AND deleted_at IS NULL
		) AND EXISTS (
			SELECT 1 FROM debts
			WHERE id = $9 AND business_id = $1 AND customer_id = $2 AND deleted_at IS NULL
		)`
	}

	debtIDExpr := "NULL::uuid"
	if req.DebtID != "" {
		debtIDExpr = "$9"
	}

	q := fmt.Sprintf(`
		INSERT INTO payments
			(business_id, customer_id, debt_id, amount, method, reference, notes, paid_at, idempotency_key)
		SELECT $1, $2, %s, $3, $4, $5, $6, $7, $8
		WHERE %s
		RETURNING %s
	`, debtIDExpr, existsClause, paymentSelect)

	p, err = scanPayment(db.QueryRow(ctx, q, args...))
	if err == nil {
		return p, false, nil
	}

	// Idempotency replay path: same key already exists. Fetch and return it.
	if isIdempotencyViolation(err) && req.IdempotencyKey != "" {
		existing, fetchErr := r.GetByIdempotencyKey(ctx, db, businessID, req.IdempotencyKey)
		if fetchErr != nil {
			return Payment{}, false, fetchErr
		}
		return existing, true, nil
	}

	// 0 rows from INSERT ... SELECT ... WHERE EXISTS => the existence check
	// failed. Without joining the queries, we can't tell which side; do a
	// targeted follow-up.
	if errors.Is(err, pgx.ErrNoRows) {
		if req.DebtID != "" {
			// Was it the customer or the debt that's missing?
			missing, classifyErr := r.classifyMissing(ctx, db, businessID, req.CustomerID, req.DebtID)
			if classifyErr != nil {
				return Payment{}, false, classifyErr
			}
			return Payment{}, false, missing
		}
		return Payment{}, false, ErrCustomerNotFound
	}
	return Payment{}, false, err
}

func (r *Repository) GetByIdempotencyKey(ctx context.Context, db DBTX, businessID, key string) (Payment, error) {
	q := `SELECT ` + paymentSelect + `
		FROM payments
		WHERE business_id = $1 AND idempotency_key = $2 AND deleted_at IS NULL`
	p, err := scanPayment(db.QueryRow(ctx, q, businessID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, ErrNotFound
	}
	return p, err
}

func (r *Repository) Get(ctx context.Context, businessID, id string) (Payment, error) {
	q := `SELECT ` + paymentSelect + `
		FROM payments
		WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL`
	p, err := scanPayment(r.db.QueryRow(ctx, q, businessID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, ErrNotFound
	}
	return p, err
}

// SoftDelete returns the payment's debt_id (if any) so the service can
// recompute the debt's status in the same transaction.
func (r *Repository) SoftDelete(ctx context.Context, db DBTX, businessID, id string) (debtID *string, err error) {
	q := `
		UPDATE payments SET deleted_at = NOW()
		WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL
		RETURNING debt_id`
	err = db.QueryRow(ctx, q, businessID, id).Scan(&debtID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return debtID, err
}

func (r *Repository) List(ctx context.Context, businessID string, q ListQuery, sortColumn, sortDir string) ([]Payment, int, error) {
	args := []any{businessID}
	where := []string{"business_id = $1", "deleted_at IS NULL"}

	if q.CustomerID != "" {
		args = append(args, q.CustomerID)
		where = append(where, fmt.Sprintf("customer_id = $%d", len(args)))
	}
	if q.DebtID != "" {
		args = append(args, q.DebtID)
		where = append(where, fmt.Sprintf("debt_id = $%d", len(args)))
	}
	if q.Method != "" {
		args = append(args, q.Method)
		where = append(where, fmt.Sprintf("method = $%d", len(args)))
	}
	whereSQL := strings.Join(where, " AND ")

	listSQL := fmt.Sprintf(`
		SELECT %s FROM payments
		WHERE %s
		ORDER BY %s %s, id ASC
		LIMIT $%d OFFSET $%d
	`, paymentSelect, whereSQL, sortColumn, sortDir, len(args)+1, len(args)+2)

	offset := (q.Page - 1) * q.PageSize
	rows, err := r.db.Query(ctx, listSQL, append(args, q.PageSize, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list payments: %w", err)
	}
	defer rows.Close()

	items := make([]Payment, 0, q.PageSize)
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM payments WHERE %s`, whereSQL)
	var total int
	if err := r.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count payments: %w", err)
	}
	return items, total, nil
}

// RecomputeDebtStatus reads the debt's amount + the SUM of its active payments
// and transitions status accordingly. Called inside the same transaction as
// a payment insert/void so the debt and payments stay consistent.
//
// We deliberately do not change a debt out of 'paid' status here: a
// mark-paid debt was administratively closed, and voiding a payment against
// it shouldn't silently undo that administrative action.
func (r *Repository) RecomputeDebtStatus(ctx context.Context, db DBTX, businessID, debtID string) error {
	q := `
		WITH agg AS (
			SELECT
				d.amount AS debt_amount,
				d.status AS current_status,
				COALESCE((
					SELECT SUM(p.amount) FROM payments p
					WHERE p.debt_id = d.id AND p.deleted_at IS NULL
				), 0) AS total_paid
			FROM debts d
			WHERE d.id = $1 AND d.business_id = $2 AND d.deleted_at IS NULL
		)
		UPDATE debts d
		SET
			status = CASE
				WHEN (SELECT current_status FROM agg) = 'paid' THEN 'paid'   -- preserve mark-paid
				WHEN (SELECT total_paid FROM agg) >= (SELECT debt_amount FROM agg) THEN 'paid'
				WHEN (SELECT total_paid FROM agg) > 0 THEN 'partial'
				ELSE 'pending'
			END,
			paid_at = CASE
				WHEN (SELECT current_status FROM agg) = 'paid' THEN d.paid_at  -- preserve original mark-paid timestamp
				WHEN (SELECT total_paid FROM agg) >= (SELECT debt_amount FROM agg) THEN NOW()
				ELSE NULL
			END
		WHERE d.id = $1 AND d.business_id = $2 AND d.deleted_at IS NULL`
	_, err := db.Exec(ctx, q, debtID, businessID)
	return err
}

func (r *Repository) classifyMissing(ctx context.Context, db DBTX, businessID, customerID, debtID string) (error, error) {
	var customerExists bool
	if err := db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM customers WHERE id = $1 AND business_id = $2 AND deleted_at IS NULL)`,
		customerID, businessID).Scan(&customerExists); err != nil {
		return nil, err
	}
	if !customerExists {
		return ErrCustomerNotFound, nil
	}
	return ErrDebtNotFound, nil
}

func isIdempotencyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == idempotencyConstraint
}

func methodOrDefault(m string) string {
	if m == "" {
		return "cash"
	}
	return m
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
