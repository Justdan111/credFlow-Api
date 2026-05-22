package payments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrValidation = errors.New("validation failed")

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

var sortFields = map[string]string{
	"createdAt": "created_at",
	"paidAt":    "paid_at",
	"amount":    "amount",
	"method":    "method",
}

var allowedMethods = map[string]struct{}{
	"cash": {}, "card": {}, "bank_transfer": {}, "check": {}, "mobile_money": {}, "other": {},
}

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// Create inserts a payment and recomputes the linked debt's status, both
// inside one transaction. On idempotency replay the existing payment is
// returned and no debt update happens (the previous request already did it).
func (s *Service) Create(ctx context.Context, businessID string, req CreateRequest) (Payment, bool, error) {
	if err := validateCreate(&req); err != nil {
		return Payment{}, false, err
	}
	paidAt, err := parsePaidAt(req.PaidAt)
	if err != nil {
		return Payment{}, false, err
	}

	var (
		out     Payment
		replay  bool
	)
	txErr := pgx.BeginFunc(ctx, s.repo.Pool(), func(tx pgx.Tx) error {
		var err error
		out, replay, err = s.repo.Create(ctx, tx, businessID, req, paidAt)
		if err != nil {
			return err
		}
		// Replay = the same key already created this payment in an earlier
		// request. That earlier request already updated the debt. Skip.
		if replay {
			return nil
		}
		if out.DebtID != nil {
			if err := s.repo.RecomputeDebtStatus(ctx, tx, businessID, *out.DebtID); err != nil {
				return fmt.Errorf("recompute debt status: %w", err)
			}
		}
		return nil
	})
	if txErr != nil {
		return Payment{}, false, txErr
	}
	return out, replay, nil
}

func (s *Service) Get(ctx context.Context, businessID, id string) (Payment, error) {
	return s.repo.Get(ctx, businessID, id)
}

// Delete soft-deletes (voids) the payment and recomputes the linked debt's
// status. Both happen in one transaction.
func (s *Service) Delete(ctx context.Context, businessID, id string) error {
	return pgx.BeginFunc(ctx, s.repo.Pool(), func(tx pgx.Tx) error {
		debtID, err := s.repo.SoftDelete(ctx, tx, businessID, id)
		if err != nil {
			return err
		}
		if debtID != nil {
			if err := s.repo.RecomputeDebtStatus(ctx, tx, businessID, *debtID); err != nil {
				return fmt.Errorf("recompute debt status: %w", err)
			}
		}
		return nil
	})
}

func (s *Service) List(ctx context.Context, businessID string, q ListQuery) ([]Payment, int, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = defaultPageSize
	}
	if q.PageSize > maxPageSize {
		q.PageSize = maxPageSize
	}
	if q.Method != "" {
		if _, ok := allowedMethods[q.Method]; !ok {
			return nil, 0, fmt.Errorf("%w: method must be one of cash, card, bank_transfer, check, mobile_money, other", ErrValidation)
		}
	}
	sortColumn, sortDir, err := parseSort(q.Sort)
	if err != nil {
		return nil, 0, err
	}
	return s.repo.List(ctx, businessID, q, sortColumn, sortDir)
}

func parseSort(sort string) (column, direction string, err error) {
	if sort == "" {
		return "paid_at", "DESC", nil
	}
	direction = "ASC"
	if strings.HasPrefix(sort, "-") {
		direction = "DESC"
		sort = sort[1:]
	}
	column, ok := sortFields[sort]
	if !ok {
		return "", "", fmt.Errorf("%w: sort must be one of: createdAt, paidAt, amount, method (optionally prefixed with -)", ErrValidation)
	}
	return column, direction, nil
}

func validateCreate(r *CreateRequest) error {
	if strings.TrimSpace(r.CustomerID) == "" {
		return fmt.Errorf("%w: customerId is required", ErrValidation)
	}
	if r.Amount <= 0 {
		return fmt.Errorf("%w: amount must be greater than 0", ErrValidation)
	}
	if r.Method != "" {
		if _, ok := allowedMethods[r.Method]; !ok {
			return fmt.Errorf("%w: method must be one of cash, card, bank_transfer, check, mobile_money, other", ErrValidation)
		}
	}
	return nil
}

func parsePaidAt(s string) (time.Time, error) {
	if s == "" {
		return time.Now(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: paidAt must be RFC3339 (e.g. 2026-05-22T10:00:00Z)", ErrValidation)
	}
	return t, nil
}
