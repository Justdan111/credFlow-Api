package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrUserNotFound  = errors.New("user not found")
	ErrEmailTaken    = errors.New("email already registered")
)

// DBTX is satisfied by both *pgxpool.Pool and pgx.Tx, so the same repository
// methods work whether or not the caller is inside a transaction.
type DBTX interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

func (r *Repository) CreateBusiness(ctx context.Context, db DBTX, name, industry, size string) (Business, error) {
	const q = `
		INSERT INTO businesses (name, industry, size)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''))
		RETURNING id, name, industry, size, created_at, updated_at
	`
	var b Business
	err := db.QueryRow(ctx, q, name, industry, size).
		Scan(&b.ID, &b.Name, &b.Industry, &b.Size, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

func (r *Repository) CreateUser(ctx context.Context, db DBTX, businessID, email, passwordHash, name, role string) (User, error) {
	const q = `
		INSERT INTO users (business_id, email, password_hash, name, role)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, business_id, email, name, role, password_hash, created_at, updated_at
	`
	var u User
	err := db.QueryRow(ctx, q, businessID, email, passwordHash, name, role).
		Scan(&u.ID, &u.BusinessID, &u.Email, &u.Name, &u.Role, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		// Postgres unique-violation SQLSTATE is 23505.
		// pgconn.PgError exposes structured access to it.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, ErrEmailTaken
		}
		return User{}, err
	}
	return u, nil
}

func (r *Repository) GetUserByEmail(ctx context.Context, db DBTX, email string) (User, error) {
	const q = `
		SELECT id, business_id, email, name, role, password_hash, created_at, updated_at
		FROM users
		WHERE email = $1
	`
	var u User
	err := db.QueryRow(ctx, q, email).
		Scan(&u.ID, &u.BusinessID, &u.Email, &u.Name, &u.Role, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return u, err
}

func (r *Repository) GetUserByID(ctx context.Context, db DBTX, id string) (User, error) {
	const q = `
		SELECT id, business_id, email, name, role, password_hash, created_at, updated_at
		FROM users
		WHERE id = $1
	`
	var u User
	err := db.QueryRow(ctx, q, id).
		Scan(&u.ID, &u.BusinessID, &u.Email, &u.Name, &u.Role, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return u, err
}

func (r *Repository) GetBusinessByID(ctx context.Context, db DBTX, id string) (Business, error) {
	const q = `
		SELECT id, name, industry, size, created_at, updated_at
		FROM businesses
		WHERE id = $1
	`
	var b Business
	err := db.QueryRow(ctx, q, id).
		Scan(&b.ID, &b.Name, &b.Industry, &b.Size, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}
