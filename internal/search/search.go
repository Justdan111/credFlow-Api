// Package search answers the one query the header search box asks: "find me
// anything matching this text".
//
// It is a deliberately small feature. Postgres full-text search or a separate
// index would be the answer at a different scale; at this one, a prefix and
// substring match over three tables is both sufficient and honest about what it
// does.
package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrValidation = errors.New("validation failed")

const (
	defaultLimit = 5
	maxLimit     = 20
	// Below two characters almost every row matches, which is a slow query
	// returning noise rather than a useful answer.
	minTermLength = 2
)

// Result is one hit, flattened to a shape the UI can render in a single list
// without knowing which table it came from.
type Result struct {
	Type string `json:"type"` // "customer" | "debt" | "payment"
	ID   string `json:"id"`
	// Title is the primary line, Subtitle the supporting one.
	Title    string     `json:"title"`
	Subtitle string     `json:"subtitle"`
	Amount   *float64   `json:"amount,omitempty"`
	Date     *time.Time `json:"date,omitempty"`
}

type Results struct {
	Query     string   `json:"query"`
	Customers []Result `json:"customers"`
	Debts     []Result `json:"debts"`
	Payments  []Result `json:"payments"`
}

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) Search(ctx context.Context, businessID, term string, limit int) (Results, error) {
	term = strings.TrimSpace(term)
	if len(term) < minTermLength {
		return Results{}, fmt.Errorf("%w: q must be at least %d characters", ErrValidation, minTermLength)
	}
	if limit < 1 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return s.repo.Search(ctx, businessID, term, limit)
}

func (r *Repository) Search(ctx context.Context, businessID, term string, limit int) (Results, error) {
	pattern := "%" + escapeLike(term) + "%"
	out := Results{Query: term, Customers: []Result{}, Debts: []Result{}, Payments: []Result{}}

	customers, err := r.searchCustomers(ctx, businessID, pattern, limit)
	if err != nil {
		return Results{}, err
	}
	out.Customers = customers

	debts, err := r.searchDebts(ctx, businessID, pattern, limit)
	if err != nil {
		return Results{}, err
	}
	out.Debts = debts

	payments, err := r.searchPayments(ctx, businessID, pattern, limit)
	if err != nil {
		return Results{}, err
	}
	out.Payments = payments

	return out, nil
}

func (r *Repository) searchCustomers(ctx context.Context, businessID, pattern string, limit int) ([]Result, error) {
	// ESCAPE '\' pairs with escapeLike below. ILIKE for case-insensitive match;
	// the name column is plain TEXT, unlike email which is CITEXT.
	const q = `
		SELECT id, name, COALESCE(NULLIF(company_name, ''), COALESCE(email::text, ''), COALESCE(phone, ''))
		FROM customers
		WHERE business_id = $1 AND deleted_at IS NULL
		  AND (name ILIKE $2 ESCAPE '\' OR email::text ILIKE $2 ESCAPE '\'
		       OR company_name ILIKE $2 ESCAPE '\' OR phone ILIKE $2 ESCAPE '\')
		ORDER BY name
		LIMIT $3
	`
	rows, err := r.db.Query(ctx, q, businessID, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search customers: %w", err)
	}
	defer rows.Close()

	out := []Result{}
	for rows.Next() {
		var res Result
		if err := rows.Scan(&res.ID, &res.Title, &res.Subtitle); err != nil {
			return nil, fmt.Errorf("scan customer hit: %w", err)
		}
		res.Type = "customer"
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *Repository) searchDebts(ctx context.Context, businessID, pattern string, limit int) ([]Result, error) {
	// Joined to customers so searching a person's name finds their debts, which
	// is what somebody typing into the header box actually wants.
	const q = `
		SELECT d.id, c.name, COALESCE(d.description, ''), d.amount, d.due_date
		FROM debts d
		JOIN customers c ON c.id = d.customer_id
		WHERE d.business_id = $1 AND d.deleted_at IS NULL
		  AND (c.name ILIKE $2 ESCAPE '\' OR d.description ILIKE $2 ESCAPE '\')
		ORDER BY d.due_date DESC
		LIMIT $3
	`
	rows, err := r.db.Query(ctx, q, businessID, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search debts: %w", err)
	}
	defer rows.Close()

	out := []Result{}
	for rows.Next() {
		var res Result
		var amount float64
		var due time.Time
		if err := rows.Scan(&res.ID, &res.Title, &res.Subtitle, &amount, &due); err != nil {
			return nil, fmt.Errorf("scan debt hit: %w", err)
		}
		res.Type = "debt"
		res.Amount = &amount
		res.Date = &due
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *Repository) searchPayments(ctx context.Context, businessID, pattern string, limit int) ([]Result, error) {
	const q = `
		SELECT p.id, c.name, COALESCE(p.reference, ''), p.amount, p.paid_at
		FROM payments p
		JOIN customers c ON c.id = p.customer_id
		WHERE p.business_id = $1 AND p.deleted_at IS NULL
		  AND (c.name ILIKE $2 ESCAPE '\' OR p.reference ILIKE $2 ESCAPE '\'
		       OR p.notes ILIKE $2 ESCAPE '\')
		ORDER BY p.paid_at DESC
		LIMIT $3
	`
	rows, err := r.db.Query(ctx, q, businessID, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search payments: %w", err)
	}
	defer rows.Close()

	out := []Result{}
	for rows.Next() {
		var res Result
		var amount float64
		var paidAt time.Time
		if err := rows.Scan(&res.ID, &res.Title, &res.Subtitle, &amount, &paidAt); err != nil {
			return nil, fmt.Errorf("scan payment hit: %w", err)
		}
		res.Type = "payment"
		res.Amount = &amount
		res.Date = &paidAt
		out = append(out, res)
	}
	return out, rows.Err()
}

// escapeLike neutralises the wildcards LIKE assigns special meaning.
//
// Without this, searching for "50%" matches every row beginning "50", and "_"
// matches any single character — so a user's literal text silently becomes a
// pattern. This is not an injection risk (the term is always a bind parameter),
// it is a correctness one: the search must find what was typed.
//
// The backslash is escaped first, or escaping the others would double-escape it.
func escapeLike(term string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(term)
}
