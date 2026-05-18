package debts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrValidation = errors.New("validation failed")

const (
	defaultPageSize = 20
	maxPageSize     = 100
	dateLayout      = "2006-01-02" // Go's reference date == "YYYY-MM-DD"
)

var sortFields = map[string]string{
	"createdAt": "created_at",
	"updatedAt": "updated_at",
	"dueDate":   "due_date",
	"amount":    "amount",
	"status":    "status",
}

var allowedStatuses = map[string]struct{}{
	"pending": {}, "partial": {}, "paid": {},
}

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(ctx context.Context, businessID string, req CreateRequest) (Debt, error) {
	if strings.TrimSpace(req.CustomerID) == "" {
		return Debt{}, fmt.Errorf("%w: customerId is required", ErrValidation)
	}
	if req.Amount <= 0 {
		return Debt{}, fmt.Errorf("%w: amount must be greater than 0", ErrValidation)
	}

	// issuedDate defaults to today when omitted.
	issued := time.Now()
	if req.IssuedDate != "" {
		d, err := time.Parse(dateLayout, req.IssuedDate)
		if err != nil {
			return Debt{}, fmt.Errorf("%w: issuedDate must be YYYY-MM-DD", ErrValidation)
		}
		issued = d
	}

	if req.DueDate == "" {
		return Debt{}, fmt.Errorf("%w: dueDate is required", ErrValidation)
	}
	due, err := time.Parse(dateLayout, req.DueDate)
	if err != nil {
		return Debt{}, fmt.Errorf("%w: dueDate must be YYYY-MM-DD", ErrValidation)
	}
	if due.Before(issued) {
		return Debt{}, fmt.Errorf("%w: dueDate cannot be before issuedDate", ErrValidation)
	}

	return s.repo.Create(ctx, businessID, req.CustomerID, req.Amount, req.Description, issued, due)
}

func (s *Service) Get(ctx context.Context, businessID, id string) (Debt, error) {
	return s.repo.Get(ctx, businessID, id)
}

func (s *Service) Update(ctx context.Context, businessID, id string, req UpdateRequest) (Debt, error) {
	if req.Amount != nil && *req.Amount <= 0 {
		return Debt{}, fmt.Errorf("%w: amount must be greater than 0", ErrValidation)
	}

	var due *time.Time
	if req.DueDate != nil {
		d, err := time.Parse(dateLayout, *req.DueDate)
		if err != nil {
			return Debt{}, fmt.Errorf("%w: dueDate must be YYYY-MM-DD", ErrValidation)
		}
		due = &d
	}

	return s.repo.Update(ctx, businessID, id, req.Amount, req.Description, due)
}

func (s *Service) Delete(ctx context.Context, businessID, id string) error {
	return s.repo.SoftDelete(ctx, businessID, id)
}

func (s *Service) MarkPaid(ctx context.Context, businessID, id string) (Debt, error) {
	return s.repo.MarkPaid(ctx, businessID, id)
}

func (s *Service) List(ctx context.Context, businessID string, q ListQuery) ([]Debt, int, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = defaultPageSize
	}
	if q.PageSize > maxPageSize {
		q.PageSize = maxPageSize
	}

	if q.Status != "" {
		if _, ok := allowedStatuses[q.Status]; !ok {
			return nil, 0, fmt.Errorf("%w: status must be one of pending, partial, paid", ErrValidation)
		}
	}

	sortColumn, sortDir, err := parseSort(q.Sort)
	if err != nil {
		return nil, 0, err
	}
	return s.repo.List(ctx, businessID, q, sortColumn, sortDir)
}

// ListByCustomer is the nested GET /api/customers/{id}/debts. It first
// confirms the customer exists in the tenant so a bad id 404s cleanly
// instead of silently returning an empty list.
func (s *Service) ListByCustomer(ctx context.Context, businessID, customerID string, q ListQuery) ([]Debt, int, error) {
	exists, err := s.repo.CustomerExists(ctx, businessID, customerID)
	if err != nil {
		return nil, 0, err
	}
	if !exists {
		return nil, 0, ErrCustomerNotFound
	}
	q.CustomerID = customerID
	return s.List(ctx, businessID, q)
}

func parseSort(sort string) (column, direction string, err error) {
	if sort == "" {
		return "created_at", "DESC", nil
	}
	direction = "ASC"
	if strings.HasPrefix(sort, "-") {
		direction = "DESC"
		sort = sort[1:]
	}
	column, ok := sortFields[sort]
	if !ok {
		return "", "", fmt.Errorf("%w: sort must be one of: createdAt, updatedAt, dueDate, amount, status (optionally prefixed with -)", ErrValidation)
	}
	return column, direction, nil
}
