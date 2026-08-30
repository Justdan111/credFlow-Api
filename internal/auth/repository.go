package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrUserNotFound         = errors.New("user not found")
	ErrEmailTaken           = errors.New("email already registered")
	ErrRefreshTokenNotFound = errors.New("refresh token not found")
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

// InsertRefreshToken stores the digest of a new token and returns the family it
// belongs to. Passing an empty familyID starts a new family — a new login —
// and Postgres mints the UUID via the column default, so UUID generation lives
// in exactly one place.
func (r *Repository) InsertRefreshToken(ctx context.Context, db DBTX, t RefreshToken, hash []byte, familyID, userAgent string) (string, error) {
	const q = `
		INSERT INTO refresh_tokens
			(user_id, business_id, family_id, token_hash, expires_at, absolute_expires_at, user_agent)
		VALUES ($1, $2, COALESCE(NULLIF($3, '')::uuid, gen_random_uuid()), $4, $5, $6, NULLIF($7, ''))
		RETURNING family_id
	`
	var out string
	err := db.QueryRow(ctx, q, t.UserID, t.BusinessID, familyID, hash,
		t.ExpiresAt, t.AbsoluteExpiresAt, userAgent).Scan(&out)
	return out, err
}

// ClaimRefreshToken atomically retires a token and returns it.
//
// The entire validity check lives in the WHERE clause on purpose. Postgres
// holds a row lock for the duration of the UPDATE, so of two concurrent
// refreshes exactly one can match and win. Reading the row first and updating
// it afterwards would let both callers see a valid token, and the loser would
// then trip reuse detection on a perfectly honest client.
//
// Returns ErrRefreshTokenNotFound when nothing matched. The caller must then
// call GetRefreshTokenByHash to learn whether this was an actual replay.
func (r *Repository) ClaimRefreshToken(ctx context.Context, db DBTX, hash []byte) (RefreshToken, error) {
	const q = `
		UPDATE refresh_tokens SET used_at = NOW()
		WHERE token_hash = $1
		  AND used_at IS NULL
		  AND revoked_at IS NULL
		  AND expires_at > NOW()
		  AND absolute_expires_at > NOW()
		RETURNING id, user_id, business_id, family_id, expires_at, absolute_expires_at
	`
	var t RefreshToken
	err := db.QueryRow(ctx, q, hash).Scan(
		&t.ID, &t.UserID, &t.BusinessID, &t.FamilyID, &t.ExpiresAt, &t.AbsoluteExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RefreshToken{}, ErrRefreshTokenNotFound
	}
	return t, err
}

// GetRefreshTokenByHash reads a token without changing it. Used only to
// diagnose why a claim failed.
func (r *Repository) GetRefreshTokenByHash(ctx context.Context, db DBTX, hash []byte) (RefreshToken, error) {
	const q = `
		SELECT id, user_id, business_id, family_id, expires_at, absolute_expires_at, used_at, revoked_at
		FROM refresh_tokens
		WHERE token_hash = $1
	`
	var t RefreshToken
	err := db.QueryRow(ctx, q, hash).Scan(
		&t.ID, &t.UserID, &t.BusinessID, &t.FamilyID,
		&t.ExpiresAt, &t.AbsoluteExpiresAt, &t.UsedAt, &t.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RefreshToken{}, ErrRefreshTokenNotFound
	}
	return t, err
}

// RevokeFamily ends one device's session. Called on logout, and on reuse
// detection to shut out both the attacker and the victim's stolen chain.
func (r *Repository) RevokeFamily(ctx context.Context, db DBTX, familyID string) error {
	const q = `
		UPDATE refresh_tokens SET revoked_at = NOW()
		WHERE family_id = $1 AND revoked_at IS NULL
	`
	_, err := db.Exec(ctx, q, familyID)
	return err
}

// DeleteExpiredRefreshTokens purges rows well past their expiry. Retired rows
// have to be kept for a while after expiry so that reuse detection can still
// recognise a replayed token rather than silently treating it as unknown.
func (r *Repository) DeleteExpiredRefreshTokens(ctx context.Context, db DBTX, grace time.Duration) (int64, error) {
	const q = `DELETE FROM refresh_tokens WHERE expires_at < NOW() - $1::interval`
	tag, err := db.Exec(ctx, q, grace.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
