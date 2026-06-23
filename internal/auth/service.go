package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrValidation         = errors.New("validation failed")
)

const (
	minPasswordLen = 8
	maxPasswordLen = 72 // bcrypt hard limit
)

type Service struct {
	db   *pgxpool.Pool
	repo *Repository
	jwt  *JWTService
}

func NewService(db *pgxpool.Pool, repo *Repository, jwt *JWTService) *Service {
	return &Service{db: db, repo: repo, jwt: jwt}
}

func (s *Service) Register(ctx context.Context, req RegisterRequest) (AuthResponse, error) {
	if err := validateRegister(req); err != nil {
		return AuthResponse{}, err
	}

	hash, err := HashPassword(req.Password)
	if err != nil {
		return AuthResponse{}, fmt.Errorf("hash password: %w", err)
	}

	var (
		biz  Business
		user User
	)
	// pgx.BeginFunc handles commit/rollback automatically based on the
	// returned error. Nil return = commit. Non-nil = rollback.
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
		return nil
	})
	if err != nil {
		return AuthResponse{}, err
	}

	token, err := s.jwt.Mint(user.ID, user.BusinessID, user.Role)
	if err != nil {
		return AuthResponse{}, fmt.Errorf("mint token: %w", err)
	}

	return AuthResponse{User: user, Business: biz, Token: token}, nil
}

func (s *Service) Login(ctx context.Context, req LoginRequest) (AuthResponse, error) {
	email := strings.TrimSpace(req.Email)
	if email == "" || req.Password == "" {
		return AuthResponse{}, ErrInvalidCredentials
	}

	user, err := s.repo.GetUserByEmail(ctx, s.db, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// Same error as wrong password — don't leak existence.
			return AuthResponse{}, ErrInvalidCredentials
		}
		return AuthResponse{}, err
	}

	if err := VerifyPassword(user.PasswordHash, req.Password); err != nil {
		return AuthResponse{}, ErrInvalidCredentials
	}

	biz, err := s.repo.GetBusinessByID(ctx, s.db, user.BusinessID)
	if err != nil {
		return AuthResponse{}, fmt.Errorf("load business: %w", err)
	}

	token, err := s.jwt.Mint(user.ID, user.BusinessID, user.Role)
	if err != nil {
		return AuthResponse{}, fmt.Errorf("mint token: %w", err)
	}

	return AuthResponse{User: user, Business: biz, Token: token}, nil
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
