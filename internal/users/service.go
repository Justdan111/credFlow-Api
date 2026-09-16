// Package users manages a business's team: invitations, roles and removal.
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
	// Without this an admin could mint an owner and inherit every power they
	// were not granted.
	ErrEscalation = errors.New("you cannot grant a role higher than your own")
	// A business with no owner can only be repaired with database access.
	ErrLastOwner = errors.New("a business must keep at least one owner")
	// Removing your own account is a foot-gun, and a way to cover tracks.
	ErrSelfTarget = errors.New("you cannot remove your own account")
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// roleRank makes "not higher than mine" one comparison.
var roleRank = map[string]int{
	auth.RoleMember: 1,
	auth.RoleAdmin:  2,
	auth.RoleOwner:  3,
}

// Inviter issues the link a new member uses to choose their own credential.
// Declared in the consumer so a test can record invitations without sending any.
type Inviter interface {
	// Reuses the password-reset machinery: one token type, one redemption path.
	SendInvitation(ctx context.Context, userID, email, name, invitedByName string) error
}

type Service struct {
	db      *pgxpool.Pool
	repo    *Repository
	inviter Inviter
	// Revokes a removed member's live tokens.
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

// Invite creates a member with a password nobody holds and mails them a link to
// set their own, so no administrator ever chooses somebody else's password.
func (s *Service) Invite(
	ctx context.Context, businessID, actorID, actorRole, actorName string, req InviteRequest,
) (Member, error) {
	if err := validateInvite(&req, actorRole); err != nil {
		return Member{}, err
	}

	// Hashed and discarded: no window in which a guessable credential exists.
	hash, err := randomPasswordHash()
	if err != nil {
		return Member{}, err
	}

	member, err := s.repo.Create(ctx, s.db, businessID, req.Email, req.Name, req.Role, hash, actorID)
	if err != nil {
		return Member{}, err
	}

	// Delivery failure does not roll back the member; an admin can re-invite.
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
	// An admin must not strip an owner of the authority that outranks them.
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

// Remove soft-deletes a member and ends their sessions in one transaction.
//
// Their access token survives until it expires — at most JWT_TTL — because a
// stateless token cannot be recalled. The short TTL is what bounds that.
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
			// Counted inside the transaction so it cannot race another removal.
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

// validateInvite normalises and checks the request in place. Role defaults to
// member, so a forgotten field cannot quietly mint an administrator.
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

// randomPasswordHash hashes 32 random bytes; the plaintext is never returned.
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
