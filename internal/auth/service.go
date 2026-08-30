package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrValidation         = errors.New("validation failed")

	// ErrInvalidRefreshToken covers unknown, expired and already-revoked
	// tokens. They are deliberately indistinguishable to the caller.
	ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")

	// ErrRefreshReuse means a retired token was presented again. The family
	// has been revoked by the time this is returned.
	ErrRefreshReuse = errors.New("refresh token reuse detected")

	// errClaimMissed is internal: it unwinds the rotation transaction without
	// treating the situation as a failure, so diagnosis can run outside it.
	errClaimMissed = errors.New("refresh token claim matched no row")
)

const (
	minPasswordLen = 8
	maxPasswordLen = 72 // bcrypt hard limit
)

type Service struct {
	db   *pgxpool.Pool
	repo *Repository
	jwt  *JWTService

	// refreshTTL is one token's lifetime; absoluteTTL caps the whole family,
	// so rotating forever cannot keep a session alive indefinitely.
	refreshTTL  time.Duration
	absoluteTTL time.Duration
}

func NewService(db *pgxpool.Pool, repo *Repository, jwt *JWTService, refreshTTL, absoluteTTL time.Duration) *Service {
	return &Service{
		db:          db,
		repo:        repo,
		jwt:         jwt,
		refreshTTL:  refreshTTL,
		absoluteTTL: absoluteTTL,
	}
}

// issueRefreshToken mints a token and stores only its digest. An empty
// familyID starts a new family (a fresh login); otherwise the token joins an
// existing rotation chain and inherits its absolute deadline unchanged.
func (s *Service) issueRefreshToken(ctx context.Context, db DBTX, user User, familyID string, absExpiry time.Time, userAgent string) (string, error) {
	plain, err := NewRefreshToken()
	if err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}

	now := time.Now()
	if familyID == "" {
		absExpiry = now.Add(s.absoluteTTL)
	}
	expiresAt := now.Add(s.refreshTTL)
	// Never let a link outlive its family: with a 30d token TTL and a 90d
	// family cap, the last token before the deadline must expire at it.
	if expiresAt.After(absExpiry) {
		expiresAt = absExpiry
	}

	rt := RefreshToken{
		UserID:            user.ID,
		BusinessID:        user.BusinessID,
		ExpiresAt:         expiresAt,
		AbsoluteExpiresAt: absExpiry,
	}
	if _, err := s.repo.InsertRefreshToken(ctx, db, rt, HashRefreshToken(plain), familyID, userAgent); err != nil {
		return "", fmt.Errorf("store refresh token: %w", err)
	}
	return plain, nil
}

// Refresh rotates a refresh token: the presented token is retired and a
// successor issued in the same family, along with a new access token. It
// returns the response and the new plaintext refresh token for the cookie.
func (s *Service) Refresh(ctx context.Context, plain, userAgent string) (AuthResponse, string, error) {
	hash := HashRefreshToken(plain)

	var (
		out      AuthResponse
		newPlain string
	)
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// Claim and issue-successor share one transaction, so a crash between
		// them cannot retire a token without minting its replacement.
		claimed, err := s.repo.ClaimRefreshToken(ctx, tx, hash)
		if err != nil {
			if errors.Is(err, ErrRefreshTokenNotFound) {
				return errClaimMissed
			}
			return err
		}

		user, err := s.repo.GetUserByID(ctx, tx, claimed.UserID)
		if err != nil {
			return err
		}
		biz, err := s.repo.GetBusinessByID(ctx, tx, user.BusinessID)
		if err != nil {
			return fmt.Errorf("load business: %w", err)
		}

		newPlain, err = s.issueRefreshToken(ctx, tx, user, claimed.FamilyID, claimed.AbsoluteExpiresAt, userAgent)
		if err != nil {
			return err
		}

		access, err := s.jwt.Mint(user.ID, user.BusinessID, user.Role)
		if err != nil {
			return fmt.Errorf("mint token: %w", err)
		}

		out = AuthResponse{User: user, Business: biz, AccessToken: access}
		return nil
	})

	// The claim matched nothing, so the transaction changed nothing and has
	// rolled back. Diagnose outside it: BeginFunc rolls back on any non-nil
	// error, which would undo the very revocation reuse detection needs.
	if errors.Is(err, errClaimMissed) {
		return AuthResponse{}, "", s.diagnoseFailedClaim(ctx, hash)
	}
	if err != nil {
		return AuthResponse{}, "", err
	}
	return out, newPlain, nil
}

// diagnoseFailedClaim decides why a claim matched no row, and revokes the
// family when the answer is a replay.
func (s *Service) diagnoseFailedClaim(ctx context.Context, hash []byte) error {
	existing, err := s.repo.GetRefreshTokenByHash(ctx, s.db, hash)
	if err != nil {
		// Unknown or already purged: forged, or long dead.
		return ErrInvalidRefreshToken
	}

	if existing.UsedAt != nil && existing.RevokedAt == nil {
		// A retired token came back. Either a stolen copy is being replayed,
		// or an honest client lost our response. The two are indistinguishable,
		// so end the session and make the user log in again.
		if err := s.repo.RevokeFamily(ctx, s.db, existing.FamilyID); err != nil {
			return fmt.Errorf("revoke family: %w", err)
		}
		return ErrRefreshReuse
	}

	// Already revoked, or expired.
	return ErrInvalidRefreshToken
}

// Logout revokes the family the presented token belongs to, ending that one
// device's session. It is idempotent: an unknown token is already logged out.
func (s *Service) Logout(ctx context.Context, plain string) error {
	t, err := s.repo.GetRefreshTokenByHash(ctx, s.db, HashRefreshToken(plain))
	if err != nil {
		if errors.Is(err, ErrRefreshTokenNotFound) {
			return nil
		}
		return err
	}
	return s.repo.RevokeFamily(ctx, s.db, t.FamilyID)
}

// CleanupExpiredTokens purges rows well past expiry. Retired rows are kept for
// a grace period so reuse detection can still recognise a replay.
func (s *Service) CleanupExpiredTokens(ctx context.Context, grace time.Duration) (int64, error) {
	return s.repo.DeleteExpiredRefreshTokens(ctx, s.db, grace)
}

// Register creates a business and its owner, and opens the first session.
// It returns the response and the plaintext refresh token for the cookie.
func (s *Service) Register(ctx context.Context, req RegisterRequest, userAgent string) (AuthResponse, string, error) {
	if err := validateRegister(req); err != nil {
		return AuthResponse{}, "", err
	}

	hash, err := HashPassword(req.Password)
	if err != nil {
		return AuthResponse{}, "", fmt.Errorf("hash password: %w", err)
	}

	var (
		biz     Business
		user    User
		refresh string
	)
	// pgx.BeginFunc handles commit/rollback automatically based on the
	// returned error. Nil return = commit. Non-nil = rollback. The refresh
	// token is issued inside the transaction so a failure cannot leave a
	// session pointing at a business that was never created.
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		biz, err = s.repo.CreateBusiness(ctx, tx, req.BusinessName, req.Industry, req.Size)
		if err != nil {
			return fmt.Errorf("create business: %w", err)
		}
		user, err = s.repo.CreateUser(ctx, tx, biz.ID, req.Email, hash, req.Name, RoleOwner)
		if err != nil {
			return err
		}
		// Empty familyID starts a new family.
		refresh, err = s.issueRefreshToken(ctx, tx, user, "", time.Time{}, userAgent)
		return err
	})
	if err != nil {
		return AuthResponse{}, "", err
	}

	token, err := s.jwt.Mint(user.ID, user.BusinessID, user.Role)
	if err != nil {
		return AuthResponse{}, "", fmt.Errorf("mint token: %w", err)
	}

	return AuthResponse{User: user, Business: biz, AccessToken: token}, refresh, nil
}

// Login verifies credentials and opens a new session (a new token family), so
// signing in on a second device leaves the first one alone.
func (s *Service) Login(ctx context.Context, req LoginRequest, userAgent string) (AuthResponse, string, error) {
	email := strings.TrimSpace(req.Email)
	if email == "" || req.Password == "" {
		return AuthResponse{}, "", ErrInvalidCredentials
	}

	user, err := s.repo.GetUserByEmail(ctx, s.db, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// Same error as wrong password — don't leak existence.
			return AuthResponse{}, "", ErrInvalidCredentials
		}
		return AuthResponse{}, "", err
	}

	if err := VerifyPassword(user.PasswordHash, req.Password); err != nil {
		return AuthResponse{}, "", ErrInvalidCredentials
	}

	biz, err := s.repo.GetBusinessByID(ctx, s.db, user.BusinessID)
	if err != nil {
		return AuthResponse{}, "", fmt.Errorf("load business: %w", err)
	}

	refresh, err := s.issueRefreshToken(ctx, s.db, user, "", time.Time{}, userAgent)
	if err != nil {
		return AuthResponse{}, "", err
	}

	token, err := s.jwt.Mint(user.ID, user.BusinessID, user.Role)
	if err != nil {
		return AuthResponse{}, "", fmt.Errorf("mint token: %w", err)
	}

	return AuthResponse{User: user, Business: biz, AccessToken: token}, refresh, nil
}

func (s *Service) Me(ctx context.Context, userID string) (AuthResponse, error) {
	user, err := s.repo.GetUserByID(ctx, s.db, userID)
	if err != nil {
		return AuthResponse{}, err
	}
	biz, err := s.repo.GetBusinessByID(ctx, s.db, user.BusinessID)
	if err != nil {
		return AuthResponse{}, fmt.Errorf("load business: %w", err)
	}
	return AuthResponse{User: user, Business: biz}, nil
}

func validateRegister(r RegisterRequest) error {
	r.Email = strings.TrimSpace(r.Email)
	r.BusinessName = strings.TrimSpace(r.BusinessName)
	r.Name = strings.TrimSpace(r.Name)

	if r.BusinessName == "" {
		return fmt.Errorf("%w: businessName is required", ErrValidation)
	}
	if r.Name == "" {
		return fmt.Errorf("%w: name is required", ErrValidation)
	}
	if _, err := mail.ParseAddress(r.Email); err != nil {
		return fmt.Errorf("%w: email is not a valid address", ErrValidation)
	}
	if len(r.Password) < minPasswordLen {
		return fmt.Errorf("%w: password must be at least %d characters", ErrValidation, minPasswordLen)
	}
	if len(r.Password) > maxPasswordLen {
		return fmt.Errorf("%w: password must be at most %d characters", ErrValidation, maxPasswordLen)
	}
	return nil
}
