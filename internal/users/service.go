// Package users manages a business's team.
//
// Three roles have existed since the first migration and RequireRole enforces
// them, but until this package there was no way to create a second user: the
// only call to CreateUser was in register, always with `owner`. The role system
// was unreachable in practice.
package users

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Justdan111/credflow-api/internal/auth"
)

var (
	ErrValidation = errors.New("validation failed")
	// ErrEscalation guards the rule that makes admin meaningfully weaker than
	// owner. Without it, an admin could mint an owner and inherit every power
	// they were not granted.
	ErrEscalation = errors.New("you cannot grant a role higher than your own")
	// ErrLastOwner stops a business being left with nobody who can administer
	// it — a state only direct database access could repair.
	ErrLastOwner = errors.New("a business must keep at least one owner")
	// ErrSelfTarget covers removing your own account: a foot-gun for a real
	// user, and for a stolen token a way to cover its tracks.
	ErrSelfTarget = errors.New("you cannot remove your own account")
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// roleRank orders the roles so "not higher than mine" is one comparison rather
// than a table of special cases.
var roleRank = map[string]int{
	auth.RoleMember: 1,
	auth.RoleAdmin:  2,
	auth.RoleOwner:  3,
}

// Inviter issues the password-reset link a new member uses to choose their own
// credential.
//
// Declared here, in the consumer, so this package depends on a two-method
// interface rather than on the whole auth service — and so a test can record
// invitations without sending anything.
type Inviter interface {
	// SendInvitation issues a single-use reset token for the user and mails the
	// link. It reuses the password-reset machinery deliberately: one token
	// type, one expiry policy, one redemption path to get right.
	SendInvitation(ctx context.Context, userID, email, name, invitedByName string) error
}

type Service struct {
	db      *pgxpool.Pool
	repo    *Repository
	inviter Inviter
	// sessions revokes a removed member's live tokens. Same reasoning as
	// Inviter: a narrow interface, declared by the consumer.
	sessions SessionRevoker
}

// SessionRevoker ends every session belonging to a user.
type SessionRevoker interface {
	RevokeAllUserSessions(ctx context.Context, db auth.DBTX, userID string) error
}

func NewService(db *pgxpool.Pool, repo *Repository, inviter Inviter, sessions SessionRevoker) *Service {
	return &Service{db: db, repo: repo, inviter: inviter, sessions: sessions}
}

func (s *Service) List(ctx context.Context, businessID string, q ListQuery) ([]Member, int, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = defaultPageSize
	}
	if q.PageSize > maxPageSize {
		q.PageSize = maxPageSize
	}
	if q.Role != "" {
		if _, ok := roleRank[q.Role]; !ok {
			return nil, 0, fmt.Errorf("%w: role must be one of owner, admin, member", ErrValidation)
		}
	}
	return s.repo.List(ctx, businessID, q)
}

func (s *Service) Get(ctx context.Context, businessID, userID string) (Member, error) {
	return s.repo.Get(ctx, businessID, userID)
}

// Invite creates a member and mails them a link to set their own password.
//
// The row is created with a password nobody holds — see randomPassword — so
// the account exists but cannot be signed into until the invitee redeems the
// link. That avoids the pattern where an administrator picks somebody else's
// password and sends it over a chat app, where it stays forever.
func (s *Service) Invite(
	ctx context.Context, businessID, actorID, actorRole, actorName string, req InviteRequest,
) (Member, error) {
	if err := validateInvite(&req, actorRole); err != nil {
		return Member{}, err
	}

	// A password of 32 random bytes that is hashed and immediately discarded.
	// The account is unusable until the invitee sets their own, and there is no
	// window in which a default or guessable credential exists.
	hash, err := randomPasswordHash()
	if err != nil {
		return Member{}, err
	}

	member, err := s.repo.Create(ctx, s.db, businessID, req.Email, req.Name, req.Role, hash, actorID)
	if err != nil {
		return Member{}, err
	}

	// Delivery failure does not roll back the member: an admin can re-invite,
	// which issues a fresh token, exactly as requesting a second reset link
	// does. Failing here would leave the caller unsure whether the user exists.
	if err := s.inviter.SendInvitation(ctx, member.ID, member.Email, member.Name, actorName); err != nil {
		return member, nil
	}
	return member, nil
}

// Update changes a member's name or role.
func (s *Service) Update(
	ctx context.Context, businessID, actorID, actorRole, targetID string, req UpdateRequest,
) (Member, error) {
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			return Member{}, fmt.Errorf("%w: name cannot be empty", ErrValidation)
		}
		req.Name = &trimmed
	}

	if req.Role == nil {
		return s.repo.Update(ctx, businessID, targetID, req.Name, nil)
	}

	role := strings.TrimSpace(*req.Role)
	if _, ok := roleRank[role]; !ok {
		return Member{}, fmt.Errorf("%w: role must be one of owner, admin, member", ErrValidation)
	}
	if err := checkGrant(actorRole, role); err != nil {
		return Member{}, err
	}

	target, err := s.repo.Get(ctx, businessID, targetID)
	if err != nil {
		return Member{}, err
	}
	// Demoting somebody who outranks you would let an admin strip an owner of
	// the very authority that put them above the admin.
	if roleRank[target.Role] > roleRank[actorRole] {
		return Member{}, ErrEscalation
	}

	if target.Role == auth.RoleOwner && role != auth.RoleOwner {
		owners, err := s.repo.CountByRole(ctx, s.db, businessID, auth.RoleOwner)
		if err != nil {
			return Member{}, err
		}
		if owners <= 1 {
			return Member{}, ErrLastOwner
		}
	}

	return s.repo.Update(ctx, businessID, targetID, req.Name, &role)
}

// Remove soft-deletes a member and ends their sessions.
//
// Both happen in one transaction: a removed user whose refresh token still
// works is not removed. Their access token survives until it expires — at most
// JWT_TTL, 15 minutes by default — because a stateless token cannot be recalled.
// That is the price of stateless auth, and the short TTL is what bounds it.
func (s *Service) Remove(ctx context.Context, businessID, actorID, actorRole, targetID string) error {
	if actorID == targetID {
		return ErrSelfTarget
	}

	target, err := s.repo.Get(ctx, businessID, targetID)
	if err != nil {
		return err
	}
	if roleRank[target.Role] > roleRank[actorRole] {
		return ErrEscalation
	}

	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if target.Role == auth.RoleOwner {
			// Counted inside the transaction so it cannot race a concurrent
			// removal and leave zero owners behind.
			owners, err := s.repo.CountByRole(ctx, tx, businessID, auth.RoleOwner)
			if err != nil {
				return err
			}
			if owners <= 1 {
				return ErrLastOwner
			}
		}
		if err := s.repo.SoftDelete(ctx, tx, businessID, targetID); err != nil {
			return err
		}
		return s.sessions.RevokeAllUserSessions(ctx, tx, targetID)
	})
}

// ActorByID satisfies audit.ActorLookup.
func (s *Service) ActorByID(ctx context.Context, userID string) (string, string, error) {
	return s.repo.ActorByID(ctx, userID)
}

// validateInvite normalises and checks the request in place, so Invite reads as
// the sequence of writes it performs rather than a wall of guards.
//
// Role defaults to member: the least privilege that still lets somebody use the
// product, so a forgotten field cannot quietly mint an administrator.
func validateInvite(req *InviteRequest, actorRole string) error {
	req.Email = strings.TrimSpace(req.Email)
	req.Name = strings.TrimSpace(req.Name)
	req.Role = strings.TrimSpace(req.Role)

	if req.Name == "" {
		return fmt.Errorf("%w: name is required", ErrValidation)
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		return fmt.Errorf("%w: email is not a valid address", ErrValidation)
	}
	if req.Role == "" {
		req.Role = auth.RoleMember
	}
	if _, ok := roleRank[req.Role]; !ok {
		return fmt.Errorf("%w: role must be one of owner, admin, member", ErrValidation)
	}
	return checkGrant(actorRole, req.Role)
}

// checkGrant enforces "never above your own rank".
func checkGrant(actorRole, targetRole string) error {
	if roleRank[targetRole] > roleRank[actorRole] {
		return ErrEscalation
	}
	return nil
}

// randomPasswordHash produces a hash of 32 random bytes. The plaintext is never
// returned, logged, or stored — it exists only long enough to be hashed.
func randomPasswordHash() (string, error) {
	plain, err := auth.NewResetToken() // 256 bits, URL-safe
	if err != nil {
		return "", fmt.Errorf("generate placeholder password: %w", err)
	}
	hash, err := auth.HashPassword(plain)
	if err != nil {
		return "", fmt.Errorf("hash placeholder password: %w", err)
	}
	return hash, nil
}
