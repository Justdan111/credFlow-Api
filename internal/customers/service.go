package customers

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

var ErrValidation = errors.New("validation failed")

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// sortFields maps the public API sort name to the actual SQL column.
// Anything not in this map is rejected. User input never reaches ORDER BY.
var sortFields = map[string]string{
	"createdAt":   "created_at",
	"updatedAt":   "updated_at",
	"name":        "name",
	"riskLevel":   "risk_level",
	"creditLimit": "credit_limit",
}

var allowedRiskLevels = map[string]struct{}{
	"low": {}, "medium": {}, "high": {},
}

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(ctx context.Context, businessID string, req CreateRequest) (Customer, error) {
	if err := validateCreate(&req); err != nil {
		return Customer{}, err
	}
	return s.repo.Create(ctx, businessID, req)
}

func (s *Service) Get(ctx context.Context, businessID, id string) (Customer, error) {
	return s.repo.Get(ctx, businessID, id)
}

func (s *Service) Update(ctx context.Context, businessID, id string, req UpdateRequest) (Customer, error) {
	if err := validateUpdate(&req); err != nil {
		return Customer{}, err
	}
	return s.repo.Update(ctx, businessID, id, req)
}

func (s *Service) Delete(ctx context.Context, businessID, id string) error {
	return s.repo.SoftDelete(ctx, businessID, id)
}

func (s *Service) List(ctx context.Context, businessID string, q ListQuery) ([]Customer, int, error) {
	// Clamp pagination.
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = defaultPageSize
	}
	if q.PageSize > maxPageSize {
		q.PageSize = maxPageSize
	}

	// Validate filter.
	if q.RiskLevel != "" {
		if _, ok := allowedRiskLevels[q.RiskLevel]; !ok {
			return nil, 0, fmt.Errorf("%w: riskLevel must be one of low, medium, high", ErrValidation)
		}
	}

	// Validate + translate sort.
	sortColumn, sortDir, err := parseSort(q.Sort)
	if err != nil {
		return nil, 0, err
	}

	return s.repo.List(ctx, businessID, q, sortColumn, sortDir)
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
		return "", "", fmt.Errorf("%w: sort must be one of: createdAt, updatedAt, name, riskLevel, creditLimit (optionally prefixed with -)", ErrValidation)
	}
	return column, direction, nil
}

func validateCreate(r *CreateRequest) error {
	r.Name = strings.TrimSpace(r.Name)
	r.Email = strings.TrimSpace(r.Email)

	if r.Name == "" {
		return fmt.Errorf("%w: name is required", ErrValidation)
	}
	if r.Email != "" {
		if _, err := mail.ParseAddress(r.Email); err != nil {
			return fmt.Errorf("%w: email is not a valid address", ErrValidation)
		}
	}
	if r.RiskLevel == "" {
		r.RiskLevel = "low"
	}
	if _, ok := allowedRiskLevels[r.RiskLevel]; !ok {
		return fmt.Errorf("%w: riskLevel must be one of low, medium, high", ErrValidation)
	}
	if r.CreditLimit < 0 {
		return fmt.Errorf("%w: creditLimit cannot be negative", ErrValidation)
	}
	return nil
}

func validateUpdate(r *UpdateRequest) error {
	if r.Name != nil {
		trimmed := strings.TrimSpace(*r.Name)
		if trimmed == "" {
			return fmt.Errorf("%w: name cannot be empty", ErrValidation)
		}
		r.Name = &trimmed
	}
	if r.Email != nil && *r.Email != "" {
		if _, err := mail.ParseAddress(*r.Email); err != nil {
			return fmt.Errorf("%w: email is not a valid address", ErrValidation)
		}
	}
	if r.RiskLevel != nil {
		if _, ok := allowedRiskLevels[*r.RiskLevel]; !ok {
			return fmt.Errorf("%w: riskLevel must be one of low, medium, high", ErrValidation)
		}
	}
	if r.CreditLimit != nil && *r.CreditLimit < 0 {
		return fmt.Errorf("%w: creditLimit cannot be negative", ErrValidation)
	}
	return nil
}
