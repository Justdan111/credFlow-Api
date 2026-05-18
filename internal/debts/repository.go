package debts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound         = errors.New("debt not found")
	ErrCustomerNotFound = errors.New("customer not found")
	ErrNoFields         = errors.New("no fields to update")
	ErrAlreadyPaid      = errors.New("debt is already paid")
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// debtSelect lists the columns plus the two derived values. Keeping it in
// one place means every read returns a consistently-shaped row.
const debtSelect = `
	id, business_id, customer_id, amount,
	CASE WHEN status = 'paid' THEN 0 ELSE amount END AS amount_remaining,
	description, status,
	(due_date < CURRENT_DATE AND status <> 'paid') AS overdue,
	issued_date, due_date, paid_at, created_at, updated_at
`

func scanDebt(row pgx.Row) (Debt, error) {
	var d Debt
	err := row.Scan(
		&d.ID, &d.BusinessID, &d.CustomerID, &d.Amount, &d.AmountRemaining,
		&d.Description, &d.Status, &d.Overdue,
		&d.IssuedDate, &d.DueDate, &d.PaidAt, &d.CreatedAt, &d.UpdatedAt,
	)
	return d, err
}

// Create inserts a debt only if the customer exists, is active, and belongs
// to this tenant — all in a single statement. The INSERT...SELECT...WHERE
// EXISTS pattern avoids a check-then-insert race condition.
func (r *Repository) Create(ctx context.Context, businessID, customerID string, amount float64, description string, issued, due time.Time) (Debt, error) {
	q := `
		INSERT INTO debts (business_id, customer_id, amount, description, issued_date, due_date)
		SELECT $1, $2, $3, NULLIF($4, ''), $5, $6
		WHERE EXISTS (
			SELECT 1 FROM customers
			WHERE id = $2 AND business_id = $1 AND deleted_at IS NULL
		)
		RETURNING ` + debtSelect

	d, err := scanDebt(r.db.QueryRow(ctx, q, businessID, customerID, amount, description, issued, due))
	if errors.Is(err, pgx.ErrNoRows) {
		// No row inserted => the WHERE EXISTS failed => customer not valid.
		return Debt{}, ErrCustomerNotFound
	}
	return d, err
}

func (r *Repository) Get(ctx context.Context, businessID, id string) (Debt, error) {
	q := `SELECT ` + debtSelect + `
		FROM debts
		WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL`
	d, err := scanDebt(r.db.QueryRow(ctx, q, businessID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Debt{}, ErrNotFound
	}
	return d, err
}

func (r *Repository) Update(ctx context.Context, businessID, id string, amount *float64, description *string, due *time.Time) (Debt, error) {
	var (
		sets []string
		args []any
	)
	add := func(col string, val any) {
		args = append(args, val)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}

	if amount != nil {
		add("amount", *amount)
	}
	if description != nil {
		if *description == "" {
			add("description", nil)
		} else {
			add("description", *description)
		}
	}
	if due != nil {
		add("due_date", *due)
	}
	if len(sets) == 0 {
		return Debt{}, ErrNoFields
	}

	args = append(args, businessID, id)
	q := fmt.Sprintf(`
		UPDATE debts SET %s
		WHERE business_id = $%d AND id = $%d AND deleted_at IS NULL
		RETURNING %s
	`, strings.Join(sets, ", "), len(args)-1, len(args), debtSelect)

	d, err := scanDebt(r.db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Debt{}, ErrNotFound
	}
	return d, err
}

func (r *Repository) SoftDelete(ctx context.Context, businessID, id string) error {
	q := `
		UPDATE debts SET deleted_at = NOW()
		WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL`
	tag, err := r.db.Exec(ctx, q, businessID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkPaid transitions a debt to 'paid' and stamps paid_at. The WHERE clause
// requires status <> 'paid', so a second call affects 0 rows — we then
// distinguish "not found" from "already paid" with a follow-up existence check.
func (r *Repository) MarkPaid(ctx context.Context, businessID, id string) (Debt, error) {
	q := `
		UPDATE debts
		SET status = 'paid', paid_at = NOW()
		WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL AND status <> 'paid'
		RETURNING ` + debtSelect

	d, err := scanDebt(r.db.QueryRow(ctx, q, businessID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		// 0 rows updated: either the debt doesn't exist, or it was already paid.
		existing, getErr := r.Get(ctx, businessID, id)
		if getErr != nil {
			return Debt{}, getErr // ErrNotFound
		}
		if existing.Status == "paid" {
			return Debt{}, ErrAlreadyPaid
		}
		return Debt{}, ErrNotFound
	}
	return d, err
}

// List returns a page of debts plus the total. sortColumn must come from the
// service whitelist. activeCustomerCheck, when non-empty, also requires the
// customer to exist and be active (used by the nested customer endpoint).
func (r *Repository) List(ctx context.Context, businessID string, q ListQuery, sortColumn, sortDir string) ([]Debt, int, error) {
	args := []any{businessID}
	where := []string{"business_id = $1", "deleted_at IS NULL"}

	if q.Status != "" {
		args = append(args, q.Status)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if q.CustomerID != "" {
		args = append(args, q.CustomerID)
		where = append(where, fmt.Sprintf("customer_id = $%d", len(args)))
	}
	if q.Overdue == "true" {
		where = append(where, "due_date < CURRENT_DATE AND status <> 'paid'")
	}

	whereSQL := strings.Join(where, " AND ")

	listSQL := fmt.Sprintf(`
		SELECT %s FROM debts
		WHERE %s
		ORDER BY %s %s, id ASC
		LIMIT $%d OFFSET $%d
	`, debtSelect, whereSQL, sortColumn, sortDir, len(args)+1, len(args)+2)

	offset := (q.Page - 1) * q.PageSize
	rows, err := r.db.Query(ctx, listSQL, append(args, q.PageSize, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list debts: %w", err)
	}
	defer rows.Close()

	items := make([]Debt, 0, q.PageSize)
	for rows.Next() {
		d, err := scanDebt(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM debts WHERE %s`, whereSQL)
	var total int
	if err := r.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count debts: %w", err)
	}
	return items, total, nil
}

// CustomerExists reports whether an active customer with this id exists in
// the tenant. Used by the nested endpoint to 404 cleanly before listing.
func (r *Repository) CustomerExists(ctx context.Context, businessID, customerID string) (bool, error) {
	q := `
		SELECT EXISTS (
			SELECT 1 FROM customers
			WHERE id = $1 AND business_id = $2 AND deleted_at IS NULL
		)`
	var exists bool
	err := r.db.QueryRow(ctx, q, customerID, businessID).Scan(&exists)
	return exists, err
}
