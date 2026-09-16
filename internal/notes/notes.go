// Package notes records the follow-up history against a customer.
//
// Collections is a conversation: who was called, what they promised, when to
// chase again. Without somewhere to put that, the context lives in one person's
// head and is lost the week they are away.
package notes

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
	ErrValidation       = errors.New("validation failed")
	ErrNotFound         = errors.New("note not found")
	ErrCustomerNotFound = errors.New("customer not found")
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	maxBodyLength   = 5000
)

// channels mirrors the CHECK constraint in migration 0011. The database stays
// the source of truth; this gives a 400 with a useful message instead of a 500
// from a constraint violation.
var channels = map[string]struct{}{
	"note": {}, "call": {}, "sms": {}, "email": {}, "visit": {},
}

type Note struct {
	ID         string `json:"id"`
	BusinessID string `json:"businessId"`
	CustomerID string `json:"customerId"`
	// AuthorID is null once that teammate is removed; AuthorName is captured at
	// write time so attribution survives them.
	AuthorID   *string   `json:"authorId"`
	AuthorName string    `json:"authorName"`
	Body       string    `json:"body"`
	Channel    string    `json:"channel"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type CreateRequest struct {
	Body    string `json:"body"`
	Channel string `json:"channel"`
}

type ListQuery struct {
	Page     int
	PageSize int
	Channel  string
}

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

const noteColumns = `
	id, business_id, customer_id, author_id, author_name, body, channel, created_at, updated_at
`

func scanNote(row pgx.Row) (Note, error) {
	var n Note
	err := row.Scan(&n.ID, &n.BusinessID, &n.CustomerID, &n.AuthorID, &n.AuthorName,
		&n.Body, &n.Channel, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}

// CustomerExists confirms the customer belongs to this tenant, so a note cannot
// be attached to somebody else's record and an unknown id 404s cleanly instead
// of silently returning an empty list.
func (r *Repository) CustomerExists(ctx context.Context, businessID, customerID string) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM customers
			WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL
		)
	`
	var exists bool
	err := r.db.QueryRow(ctx, q, businessID, customerID).Scan(&exists)
	return exists, err
}

func (r *Repository) Create(
	ctx context.Context, businessID, customerID, authorID, authorName, body, channel string,
) (Note, error) {
	const q = `
		INSERT INTO customer_notes
			(business_id, customer_id, author_id, author_name, body, channel)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING ` + noteColumns
	return scanNote(r.db.QueryRow(ctx, q, businessID, customerID, authorID, authorName, body, channel))
}

func (r *Repository) List(
	ctx context.Context, businessID, customerID string, q ListQuery,
) ([]Note, int, error) {
	where := ` WHERE business_id = $1 AND customer_id = $2 AND deleted_at IS NULL`
	args := []any{businessID, customerID}
	if q.Channel != "" {
		args = append(args, q.Channel)
		where += fmt.Sprintf(" AND channel = $%d", len(args))
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM customer_notes`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count notes: %w", err)
	}

	// id breaks ties: two notes written in the same second would otherwise swap
	// places between pages and one would never be seen.
	listSQL := `SELECT ` + noteColumns + ` FROM customer_notes` + where +
		fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, q.PageSize, (q.Page-1)*q.PageSize)

	rows, err := r.db.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list notes: %w", err)
	}
	defer rows.Close()

	out := make([]Note, 0, q.PageSize)
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan note: %w", err)
		}
		out = append(out, n)
	}
	return out, total, rows.Err()
}

func (r *Repository) SoftDelete(ctx context.Context, businessID, noteID string) error {
	const q = `
		UPDATE customer_notes SET deleted_at = NOW()
		WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL
	`
	tag, err := r.db.Exec(ctx, q, businessID, noteID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) Create(
	ctx context.Context, businessID, customerID, authorID, authorName string, req CreateRequest,
) (Note, error) {
	body, channel, err := validateCreate(req)
	if err != nil {
		return Note{}, err
	}

	exists, err := s.repo.CustomerExists(ctx, businessID, customerID)
	if err != nil {
		return Note{}, err
	}
	if !exists {
		return Note{}, ErrCustomerNotFound
	}

	return s.repo.Create(ctx, businessID, customerID, authorID, authorName, body, channel)
}

// validateCreate normalises the request and returns the values to store.
//
// The database CHECK already rejects an empty body; this turns that into a 400
// with a useful message instead of a 500 from a constraint violation.
func validateCreate(req CreateRequest) (body, channel string, err error) {
	body = strings.TrimSpace(req.Body)
	if body == "" {
		return "", "", fmt.Errorf("%w: body is required", ErrValidation)
	}
	if len(body) > maxBodyLength {
		return "", "", fmt.Errorf("%w: body must be at most %d characters", ErrValidation, maxBodyLength)
	}

	channel = strings.TrimSpace(req.Channel)
	if channel == "" {
		channel = "note"
	}
	if _, ok := channels[channel]; !ok {
		return "", "", fmt.Errorf("%w: channel must be one of note, call, sms, email, visit", ErrValidation)
	}
	return body, channel, nil
}

func (s *Service) List(
	ctx context.Context, businessID, customerID string, q ListQuery,
) ([]Note, int, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = defaultPageSize
	}
	if q.PageSize > maxPageSize {
		q.PageSize = maxPageSize
	}
	if q.Channel != "" {
		if _, ok := channels[q.Channel]; !ok {
			return nil, 0, fmt.Errorf("%w: channel must be one of note, call, sms, email, visit", ErrValidation)
		}
	}

	exists, err := s.repo.CustomerExists(ctx, businessID, customerID)
	if err != nil {
		return nil, 0, err
	}
	if !exists {
		return nil, 0, ErrCustomerNotFound
	}

	return s.repo.List(ctx, businessID, customerID, q)
}

func (s *Service) Delete(ctx context.Context, businessID, noteID string) error {
	return s.repo.SoftDelete(ctx, businessID, noteID)
}
