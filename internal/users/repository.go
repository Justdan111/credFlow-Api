package users

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound   = errors.New("user not found")
	ErrEmailTaken = errors.New("a user with this email already exists")
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// DBTX lets the caller run a write inside an existing transaction, matching the
// pattern in customers and debts.
type DBTX interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// memberColumns joins the newest refresh token per user to derive last activity.
//
// A LATERAL subquery rather than a GROUP BY over the whole join: it stops at the
// first row per user using the existing index, instead of aggregating every
// token a long-lived account has ever held.
const memberSelect = `
	SELECT u.id, u.business_id, u.email, u.name, COALESCE(u.phone, ''), u.role,
	       u.invited_by, last_session.created_at,
	       u.created_at, u.updated_at
	FROM users u
	LEFT JOIN LATERAL (
		SELECT rt.created_at
		FROM refresh_tokens rt
		WHERE rt.user_id = u.id
		ORDER BY rt.created_at DESC
		LIMIT 1
	) AS last_session ON TRUE
`

func scanMember(row pgx.Row) (Member, error) {
	var m Member
	err := row.Scan(
		&m.ID, &m.BusinessID, &m.Email, &m.Name, &m.Phone, &m.Role,
		&m.InvitedBy, &m.LastActiveAt, &m.CreatedAt, &m.UpdatedAt,
	)
	return m, err
}

func (r *Repository) Get(ctx context.Context, businessID, userID string) (Member, error) {
	q := memberSelect + ` WHERE u.business_id = $1 AND u.id = $2 AND u.deleted_at IS NULL`
	m, err := scanMember(r.db.QueryRow(ctx, q, businessID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	return m, err
}

func (r *Repository) List(ctx context.Context, businessID string, q ListQuery) ([]Member, int, error) {
	where := ` WHERE u.business_id = $1 AND u.deleted_at IS NULL`
	args := []any{businessID}
	if q.Role != "" {
		args = append(args, q.Role)
		where += fmt.Sprintf(" AND u.role = $%d", len(args))
	}

	countSQL := `SELECT COUNT(*) FROM users u` + where
	var total int
	if err := r.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}

	listSQL := memberSelect + where +
		fmt.Sprintf(" ORDER BY u.created_at ASC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, q.PageSize, (q.Page-1)*q.PageSize)

	rows, err := r.db.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	members := make([]Member, 0, q.PageSize)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan user: %w", err)
		}
		members = append(members, m)
	}
	return members, total, rows.Err()
}

// Create inserts a member. The password hash is supplied by the service, which
// generates one nobody holds — the invitee sets their own through the reset flow.
func (r *Repository) Create(
	ctx context.Context, db DBTX, businessID, email, name, role, passwordHash, invitedBy string,
) (Member, error) {
	const q = `
		INSERT INTO users (business_id, email, name, role, password_hash, invited_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, business_id, email, name, COALESCE(phone, ''), role,
		          invited_by, NULL::timestamptz, created_at, updated_at
	`
	m, err := scanMember(db.QueryRow(ctx, q, businessID, email, name, role, passwordHash, invitedBy))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Member{}, ErrEmailTaken
		}
		return Member{}, err
	}
	return m, nil
}

// Update changes name and role. business_id is in the WHERE clause, so an id
// from another tenant simply matches nothing.
func (r *Repository) Update(ctx context.Context, businessID, userID string, name, role *string) (Member, error) {
	const q = `
		UPDATE users
		SET name = COALESCE($3, name),
		    role = COALESCE($4, role)
		WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL
		RETURNING id
	`
	var id string
	err := r.db.QueryRow(ctx, q, businessID, userID, name, role).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, err
	}
	// Re-read through the list projection so the response carries last activity
	// rather than a second, thinner shape of the same resource.
	return r.Get(ctx, businessID, userID)
}

// SoftDelete marks the member removed and returns whether a row was affected.
func (r *Repository) SoftDelete(ctx context.Context, db DBTX, businessID, userID string) error {
	const q = `
		UPDATE users SET deleted_at = NOW()
		WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL
	`
	tag, err := db.Exec(ctx, q, businessID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CountByRole powers the last-owner guard.
func (r *Repository) CountByRole(ctx context.Context, db DBTX, businessID, role string) (int, error) {
	const q = `
		SELECT COUNT(*) FROM users
		WHERE business_id = $1 AND role = $2 AND deleted_at IS NULL
	`
	var n int
	err := db.QueryRow(ctx, q, businessID, role).Scan(&n)
	return n, err
}

// ActorByID resolves the identity denormalised onto an audit entry. It ignores
// deleted_at: an entry written by someone since removed must still name them.
func (r *Repository) ActorByID(ctx context.Context, userID string) (string, string, error) {
	const q = `SELECT email, name FROM users WHERE id = $1`
	var email, name string
	err := r.db.QueryRow(ctx, q, userID).Scan(&email, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return email, name, err
}
