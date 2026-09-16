package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
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

	// ErrPasswordMismatch is returned when the supplied current password is
	// wrong on a change-password request.
	ErrPasswordMismatch = errors.New("current password is incorrect")
	// ErrSessionNotFound covers both an unknown session and one belonging to
	// another user — the two are deliberately indistinguishable.
	ErrSessionNotFound = errors.New("session not found")

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

	mailer     Mailer
	appBaseURL string
	resetTTL   time.Duration

	// refreshTTL is one token's lifetime; absoluteTTL caps the whole family,
	// so rotating forever cannot keep a session alive indefinitely.
	refreshTTL  time.Duration
	absoluteTTL time.Duration
}

// Mailer is the subset of pkg/mailer this package needs. Declaring it here
// rather than importing keeps the dependency pointing inwards: the concrete
// mailer is chosen in main.go, and tests can substitute a recorder.
type Mailer interface {
	SendPasswordReset(ctx context.Context, to, name, resetURL string) error
	SendInvitation(ctx context.Context, to, name, inviterName, setupURL string) error
}

func NewService(db *pgxpool.Pool, repo *Repository, jwt *JWTService, refreshTTL, absoluteTTL time.Duration, m Mailer, appBaseURL string, resetTTL time.Duration) *Service {
	return &Service{
		db:          db,
		repo:        repo,
		jwt:         jwt,
		refreshTTL:  refreshTTL,
		absoluteTTL: absoluteTTL,
		mailer:      m,
		appBaseURL:  strings.TrimRight(appBaseURL, "/"),
		resetTTL:    resetTTL,
	}
}

// GetProfile returns the /api/auth/me view.
func (s *Service) GetProfile(ctx context.Context, userID string) (Profile, error) {
	u, phone, err := s.repo.GetProfile(ctx, s.db, userID)
	if err != nil {
		return Profile{}, err
	}
	return Profile{
		ID: u.ID, Email: u.Email, Name: u.Name, Phone: phone, Role: u.Role,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}, nil
}

func (s *Service) UpdateProfile(ctx context.Context, userID string, req UpdateProfileRequest) (Profile, error) {
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return Profile{}, fmt.Errorf("%w: name cannot be empty", ErrValidation)
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		req.Name = &trimmed
	}
	if _, err := s.repo.UpdateProfile(ctx, s.db, userID, req.Name, req.Phone); err != nil {
		return Profile{}, err
	}
	return s.GetProfile(ctx, userID)
}

// ChangePassword requires the current password even though the caller is
// already authenticated: an access token may have been stolen, and this check
// is what stops it being escalated into a permanent account takeover.
//
// Every OTHER session is revoked on success. If somebody else holds a session,
// changing the password should evict them — while the caller stays signed in.
func (s *Service) ChangePassword(ctx context.Context, userID string, req ChangePasswordRequest, currentRefreshToken string) error {
	if err := validatePassword(req.NewPassword); err != nil {
		return err
	}

	user, err := s.repo.GetUserByID(ctx, s.db, userID)
	if err != nil {
		return err
	}
	if err := VerifyPassword(user.PasswordHash, req.CurrentPassword); err != nil {
		return ErrPasswordMismatch
	}

	hash, err := HashPassword(req.NewPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	// Resolve the caller's own family first so it can be spared.
	keep := ""
	if currentRefreshToken != "" {
		if fam, err := s.repo.FamilyForToken(ctx, s.db, HashRefreshToken(currentRefreshToken)); err == nil {
			keep = fam
		}
	}

	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.repo.UpdatePasswordHash(ctx, tx, userID, hash); err != nil {
			return err
		}
		if keep == "" {
			// No identifiable current session, so end them all rather than
			// leave a possibly-hostile one alive.
			return s.repo.RevokeAllUserSessions(ctx, tx, userID)
		}
		return s.repo.RevokeOtherUserSessions(ctx, tx, userID, keep)
	})
}

// ListSessions returns the user's live logins, marking the calling one.
func (s *Service) ListSessions(ctx context.Context, userID, currentRefreshToken string) ([]SessionView, error) {
	sessions, err := s.repo.ListSessions(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}

	current := ""
	if currentRefreshToken != "" {
		if fam, err := s.repo.FamilyForToken(ctx, s.db, HashRefreshToken(currentRefreshToken)); err == nil {
			current = fam
		}
	}

	out := make([]SessionView, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, SessionView{
			ID: sess.ID, UserAgent: sess.UserAgent,
			CreatedAt: sess.CreatedAt, LastActiveAt: sess.LastActiveAt,
			Current: sess.ID != "" && sess.ID == current,
		})
	}
	return out, nil
}

// RevokeSession ends one login. Scoped to the caller, so a user cannot revoke
// somebody else's session even within the same business.
func (s *Service) RevokeSession(ctx context.Context, userID, sessionID string) error {
	n, err := s.repo.RevokeFamilyForUser(ctx, s.db, userID, sessionID)
	if err != nil {
		return err
	}
	if n == 0 {
		// Unknown, already revoked, or another user's: all one answer, so the
		// response cannot be used to probe for other people's sessions.
		return ErrSessionNotFound
	}
	return nil
}

// ForgotPassword issues a reset link. It reports success whether or not the
// address exists: any difference in status or body would turn this into an
// account-enumeration oracle.
func (s *Service) ForgotPassword(ctx context.Context, req ForgotPasswordRequest) error {
	email := strings.TrimSpace(req.Email)
	if email == "" {
		return nil
	}

	user, err := s.repo.GetUserByEmail(ctx, s.db, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil // silent by design
		}
		return err
	}

	plain, err := NewResetToken()
	if err != nil {
		return err
	}

	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// Only the newest link may work; a second request must burn the first.
		if err := s.repo.InvalidateResetTokens(ctx, tx, user.ID); err != nil {
			return err
		}
		return s.repo.CreateResetToken(ctx, tx, user.ID,
			HashResetToken(plain), time.Now().Add(s.resetTTL))
	})
	if err != nil {
		return err
	}

	link := fmt.Sprintf("%s/reset-password?token=%s", s.appBaseURL, plain)
	if err := s.mailer.SendPasswordReset(ctx, user.Email, user.Name, link); err != nil {
		// The token is already stored, so a delivery failure is logged rather
		// than surfaced — telling the caller would leak that the account exists.
		log.Printf("send password reset to %s failed: %v", user.Email, err)
	}
	return nil
}

// ResetPassword redeems a token. Every session is revoked on success: whoever
// forced the reset must not keep a live one.
func (s *Service) ResetPassword(ctx context.Context, req ResetPasswordRequest) error {
	if strings.TrimSpace(req.Token) == "" {
		return ErrResetTokenInvalid
	}
	if err := validatePassword(req.NewPassword); err != nil {
		return err
	}

	hash, err := HashPassword(req.NewPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		userID, err := s.repo.ClaimResetToken(ctx, tx, HashResetToken(req.Token))
		if err != nil {
			return err
		}
		if err := s.repo.UpdatePasswordHash(ctx, tx, userID, hash); err != nil {
			return err
		}
		return s.repo.RevokeAllUserSessions(ctx, tx, userID)
	})
}

// SendInvitation issues a single-use link for a newly invited teammate to set
// their first password, and satisfies users.Inviter.
//
// It deliberately reuses the password-reset token: same entropy, same single-use
// rule, same expiry, same redemption path. A separate "invitation token" would
// be a second credential type with its own chance of getting one of those wrong,
// to solve a problem the first one already solves.
//
// Unlike ForgotPassword this returns delivery errors. There is no enumeration
// concern — the caller just created the account and knows it exists — and an
// admin who invites somebody wants to know the mail did not go out.
func (s *Service) SendInvitation(ctx context.Context, userID, email, name, invitedByName string) error {
	plain, err := NewResetToken()
	if err != nil {
		return err
	}

	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// Re-inviting must burn the previous link, exactly as a second
		// forgot-password request does.
		if err := s.repo.InvalidateResetTokens(ctx, tx, userID); err != nil {
			return err
		}
		return s.repo.CreateResetToken(ctx, tx, userID,
			HashResetToken(plain), time.Now().Add(s.resetTTL))
	})
	if err != nil {
		return err
	}

	link := fmt.Sprintf("%s/reset-password?token=%s", s.appBaseURL, plain)
	return s.mailer.SendInvitation(ctx, email, name, invitedByName, link)
}

// CleanupExpiredResetTokens is called by the existing daily job.
func (s *Service) CleanupExpiredResetTokens(ctx context.Context, grace time.Duration) (int64, error) {
	return s.repo.DeleteExpiredResetTokens(ctx, s.db, grace)
}

func validatePassword(p string) error {
	if len(p) < minPasswordLen {
		return fmt.Errorf("%w: password must be at least %d characters", ErrValidation, minPasswordLen)
	}
	if len(p) > maxPasswordLen {
		return fmt.Errorf("%w: password must be at most %d characters", ErrValidation, maxPasswordLen)
	}
	return nil
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
			// The account was removed while this session was live. Removal
			// revokes its tokens, so the claim above normally fails first;
			// this is the backstop, and it must read as a dead session rather
			// than as a missing resource.
			if errors.Is(err, ErrUserNotFound) {
				return ErrInvalidRefreshToken
			}
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
