package customers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound    = errors.New("customer not found")
	ErrEmailTaken  = errors.New("a customer with this email already exists")
	ErrNoFields    = errors.New("no fields to update")
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

const customerColumns = `
	id, business_id, name, email, phone, company_name, address,
	risk_level, credit_limit, notes, created_at, updated_at
`

func scanCustomer(row pgx.Row) (Customer, error) {
	var c Customer
	err := row.Scan(
		&c.ID, &c.BusinessID, &c.Name, &c.Email, &c.Phone, &c.CompanyName, &c.Address,
		&c.RiskLevel, &c.CreditLimit, &c.Notes, &c.CreatedAt, &c.UpdatedAt,
	)
	return c, err
}

func (r *Repository) Create(ctx context.Context, businessID string, req CreateRequest) (Customer, error) {
	q := `
		INSERT INTO customers
			(business_id, name, email, phone, company_name, address, risk_level, credit_limit, notes)
		VALUES
			($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7, $8, NULLIF($9, ''))
		RETURNING ` + customerColumns

	row := r.db.QueryRow(ctx, q,
		businessID, req.Name, req.Email, req.Phone, req.CompanyName,
		req.Address, req.RiskLevel, req.CreditLimit, req.Notes,
	)
	c, err := scanCustomer(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Customer{}, ErrEmailTaken
		}
		return Customer{}, err
	}
	return c, nil
}

func (r *Repository) Get(ctx context.Context, businessID, id string) (Customer, error) {
	q := `
		SELECT ` + customerColumns + `
		FROM customers
		WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL
	`
	c, err := scanCustomer(r.db.QueryRow(ctx, q, businessID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, ErrNotFound
	}
	return c, err
}

// Update applies non-nil fields of req via a dynamic SET clause. The set
// of column names is fixed in code — there is no path for user input to
// reach the SQL column list.
func (r *Repository) Update(ctx context.Context, businessID, id string, req UpdateRequest) (Customer, error) {
	var (
		sets []string
		args []any
	)
	add := func(col string, val any) {
		args = append(args, val)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}

	if req.Name != nil {
		add("name", *req.Name)
	}
	if req.Email != nil {
		// NULLIF inside SQL would require special handling here; instead the
		// service normalizes "" to a sentinel before reaching the repo.
		if *req.Email == "" {
			add("email", nil)
		} else {
			add("email", *req.Email)
		}
	}
	if req.Phone != nil {
		add("phone", nullIfEmpty(*req.Phone))
	}
	if req.CompanyName != nil {
		add("company_name", nullIfEmpty(*req.CompanyName))
	}
	if req.Address != nil {
		add("address", nullIfEmpty(*req.Address))
	}
	if req.RiskLevel != nil {
		add("risk_level", *req.RiskLevel)
	}
	if req.CreditLimit != nil {
		add("credit_limit", *req.CreditLimit)
	}
	if req.Notes != nil {
		add("notes", nullIfEmpty(*req.Notes))
	}

	if len(sets) == 0 {
		return Customer{}, ErrNoFields
	}

	// Tenant + id are the last two arguments. The trigger keeps updated_at fresh.
	args = append(args, businessID, id)
	q := fmt.Sprintf(`
		UPDATE customers
		SET %s
		WHERE business_id = $%d AND id = $%d AND deleted_at IS NULL
		RETURNING %s
	`, strings.Join(sets, ", "), len(args)-1, len(args), customerColumns)

	c, err := scanCustomer(r.db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, ErrNotFound
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Customer{}, ErrEmailTaken
		}
		return Customer{}, err
	}
	return c, nil
}

// SoftDelete sets deleted_at = NOW(). Returns ErrNotFound if the row was
// already gone or never existed in this tenant.
func (r *Repository) SoftDelete(ctx context.Context, businessID, id string) error {
	q := `
		UPDATE customers
		SET deleted_at = NOW()
		WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL
	`
	tag, err := r.db.Exec(ctx, q, businessID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns a page of customers and the total matching the filters.
// sortColumn must be a validated SQL identifier from the service whitelist.
func (r *Repository) List(ctx context.Context, businessID string, q ListQuery, sortColumn, sortDir string) ([]Customer, int, error) {
	args := []any{businessID}
	where := []string{"business_id = $1", "deleted_at IS NULL"}

	if q.Search != "" {
		args = append(args, "%"+q.Search+"%")
		idx := len(args)
		where = append(where, fmt.Sprintf(
			"(name ILIKE $%d OR email ILIKE $%d OR phone ILIKE $%d OR company_name ILIKE $%d)",
			idx, idx, idx, idx,
		))
	}
	if q.RiskLevel != "" {
		args = append(args, q.RiskLevel)
		where = append(where, fmt.Sprintf("risk_level = $%d", len(args)))
	}

	whereSQL := strings.Join(where, " AND ")

	// Two queries: one for the page, one for the total.
	listSQL := fmt.Sprintf(`
		SELECT %s
		FROM customers
		WHERE %s
		ORDER BY %s %s, id ASC
		LIMIT $%d OFFSET $%d
	`, customerColumns, whereSQL, sortColumn, sortDir, len(args)+1, len(args)+2)

	offset := (q.Page - 1) * q.PageSize
	listArgs := append(args, q.PageSize, offset)

	rows, err := r.db.Query(ctx, listSQL, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list customers: %w", err)
	}
	defer rows.Close()

	items := make([]Customer, 0, q.PageSize)
	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// COUNT(*) over the same filters (no ORDER/LIMIT/OFFSET).
	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM customers WHERE %s`, whereSQL)
	var total int
	if err := r.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count customers: %w", err)
	}

	return items, total, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
