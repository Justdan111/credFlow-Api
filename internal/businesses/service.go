package businesses

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Justdan111/credflow-api/internal/customers"
	"github.com/Justdan111/credflow-api/internal/debts"
)

var (
	ErrValidation = errors.New("validation failed")
	// ErrCurrencyLocked is returned when a currency change is attempted after
	// financial records exist. Changing it converts nothing, so every stored
	// amount would silently be reinterpreted in the new currency.
	ErrCurrencyLocked = errors.New("currency cannot change once debts or payments exist")
	// ErrAlreadyOnboarded stops a double-submit creating a second "first" customer.
	ErrAlreadyOnboarded = errors.New("onboarding is already complete")
)

// supportedCurrencies mirrors the CHECK constraint in migration 0007. The
// database remains the source of truth; this gives a 400 with a useful message
// instead of a 500 from a constraint violation.
var supportedCurrencies = map[string]bool{
	"NGN": true, "GHS": true, "KES": true, "ZAR": true, "USD": true,
}

type Service struct {
	db   *pgxpool.Pool
	repo *Repository
	// Onboarding goes through the customer and debt SERVICES, not their
	// repositories: the services own validation and defaulting (risk_level in
	// particular, which the database CHECK rejects when empty). Reaching for
	// the repositories directly skips that and fails at the constraint.
	customerSvc *customers.Service
	debtSvc     *debts.Service
}

func NewService(db *pgxpool.Pool, repo *Repository, customerSvc *customers.Service, debtSvc *debts.Service) *Service {
	return &Service{db: db, repo: repo, customerSvc: customerSvc, debtSvc: debtSvc}
}

// Get returns the profile with CurrencyLocked computed, so the frontend can
// disable the selector rather than offer a change the API will reject.
func (s *Service) Get(ctx context.Context, businessID string) (Business, error) {
	b, err := s.repo.Get(ctx, s.db, businessID)
	if err != nil {
		return Business{}, err
	}
	locked, err := s.repo.HasFinancialRecords(ctx, s.db, businessID)
	if err != nil {
		return Business{}, fmt.Errorf("check financial records: %w", err)
	}
	b.CurrencyLocked = locked
	return b, nil
}

func (s *Service) Update(ctx context.Context, businessID string, req UpdateRequest) (Business, error) {
	if err := validateUpdate(req); err != nil {
		return Business{}, err
	}

	var out Business
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// The lock check runs inside the transaction so it cannot race a debt
		// being created concurrently: a change that passes the check here
		// cannot be overtaken before the update commits.
		if req.Currency != nil {
			current, err := s.repo.Get(ctx, tx, businessID)
			if err != nil {
				return err
			}
			if !strings.EqualFold(current.Currency, *req.Currency) {
				locked, err := s.repo.HasFinancialRecords(ctx, tx, businessID)
				if err != nil {
					return fmt.Errorf("check financial records: %w", err)
				}
				if locked {
					return ErrCurrencyLocked
				}
			}
		}

		b, err := s.repo.Update(ctx, tx, businessID, req)
		if err != nil {
			return err
		}
		out = b
		return nil
	})
	if err != nil {
		return Business{}, err
	}

	locked, err := s.repo.HasFinancialRecords(ctx, s.db, businessID)
	if err != nil {
		return Business{}, fmt.Errorf("check financial records: %w", err)
	}
	out.CurrencyLocked = locked
	return out, nil
}

// ClearTarget removes the monthly collection target.
func (s *Service) ClearTarget(ctx context.Context, businessID string) (Business, error) {
	b, err := s.repo.ClearTarget(ctx, s.db, businessID)
	if err != nil {
		return Business{}, err
	}
	locked, err := s.repo.HasFinancialRecords(ctx, s.db, businessID)
	if err != nil {
		return Business{}, err
	}
	b.CurrencyLocked = locked
	return b, nil
}

// OnboardingStatus derives each step from real records rather than a stored
// counter, so it cannot drift out of sync with what the business actually has.
func (s *Service) OnboardingStatus(ctx context.Context, businessID string) (OnboardingStatus, error) {
	var out OnboardingStatus

	completedAt, err := s.repo.OnboardingCompletedAt(ctx, s.db, businessID)
	if err != nil {
		return out, err
	}
	hasProfile, hasCustomer, hasDebt, err := s.repo.OnboardingProgress(ctx, s.db, businessID)
	if err != nil {
		return out, fmt.Errorf("onboarding progress: %w", err)
	}

	out.Completed = completedAt != nil
	out.Steps.Business = hasProfile
	out.Steps.Customer = hasCustomer
	out.Steps.Debt = hasDebt

	switch {
	case out.Completed:
		out.CurrentStep = ""
	case !hasProfile:
		out.CurrentStep = "business"
	case !hasCustomer:
		out.CurrentStep = "customer"
	default:
		out.CurrentStep = "debt"
	}
	return out, nil
}

// Complete persists the whole onboarding payload in one transaction. All of it
// lands or none of it does: a business with a customer but no profile is worse
// than a clean retry.
func (s *Service) Complete(ctx context.Context, businessID string, req CompleteRequest) (CompleteResponse, error) {
	if err := validateComplete(req); err != nil {
		return CompleteResponse{}, err
	}

	var out CompleteResponse
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		completedAt, err := s.repo.OnboardingCompletedAt(ctx, tx, businessID)
		if err != nil {
			return err
		}
		if completedAt != nil {
			return ErrAlreadyOnboarded
		}

		// The currency lock applies here too, though a business onboarding
		// normally has no records yet.
		if req.Currency != "" {
			current, err := s.repo.Get(ctx, tx, businessID)
			if err != nil {
				return err
			}
			if !strings.EqualFold(current.Currency, req.Currency) {
				locked, err := s.repo.HasFinancialRecords(ctx, tx, businessID)
				if err != nil {
					return err
				}
				if locked {
					return ErrCurrencyLocked
				}
			}
		}

		upd := UpdateRequest{}
		if req.Industry != "" {
			upd.Industry = &req.Industry
		}
		if req.Size != "" {
			upd.Size = &req.Size
		}
		if req.Currency != "" {
			c := strings.ToUpper(req.Currency)
			upd.Currency = &c
		}
		if _, err := s.repo.Update(ctx, tx, businessID, upd); err != nil {
			return fmt.Errorf("update business: %w", err)
		}

		if req.Customer != nil {
			// Through the SERVICE, not the repository: the service owns
			// validation and defaulting, notably risk_level, which the
			// database CHECK rejects when empty.
			c, err := s.customerSvc.CreateTx(ctx, tx, businessID, customers.CreateRequest{
				Name:  req.Customer.Name,
				Email: req.Customer.Email,
				Phone: req.Customer.Phone,
			})
			if err != nil {
				if errors.Is(err, customers.ErrValidation) {
					return fmt.Errorf("%w: customer: %s", ErrValidation, err)
				}
				return fmt.Errorf("create first customer: %w", err)
			}
			out.CustomerID = &c.ID

			if req.Debt != nil {
				// The schema enforces due_date >= issued_date. Backdate the
				// issue date rather than reject a debt that is already due.
				issued := time.Now().Format("2006-01-02")
				if req.Debt.DueDate < issued {
					issued = req.Debt.DueDate
				}
				d, err := s.debtSvc.CreateTx(ctx, tx, businessID, debts.CreateRequest{
					CustomerID: c.ID,
					Amount:     req.Debt.Amount,
					IssuedDate: issued,
					DueDate:    req.Debt.DueDate,
				})
				if err != nil {
					if errors.Is(err, debts.ErrValidation) {
						return fmt.Errorf("%w: debt: %s", ErrValidation, err)
					}
					return fmt.Errorf("create first debt: %w", err)
				}
				out.DebtID = &d.ID
			}
		}

		if err := s.repo.MarkOnboardingComplete(ctx, tx, businessID); err != nil {
			return fmt.Errorf("mark complete: %w", err)
		}

		b, err := s.repo.Get(ctx, tx, businessID)
		if err != nil {
			return err
		}
		out.Business = b
		return nil
	})
	if err != nil {
		return CompleteResponse{}, err
	}

	locked, err := s.repo.HasFinancialRecords(ctx, s.db, businessID)
	if err != nil {
		return CompleteResponse{}, err
	}
	out.Business.CurrencyLocked = locked
	return out, nil
}

// SaveStep records progress so a refresh resumes where the user left off.
func (s *Service) SaveStep(ctx context.Context, businessID, step string) error {
	switch step {
	case "business", "customer", "debt", "":
	default:
		return fmt.Errorf("%w: step must be business, customer or debt", ErrValidation)
	}
	return s.repo.SetOnboardingStep(ctx, s.db, businessID, step)
}

func validateUpdate(req UpdateRequest) error {
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return fmt.Errorf("%w: name cannot be empty", ErrValidation)
	}
	if req.Currency != nil && !supportedCurrencies[strings.ToUpper(*req.Currency)] {
		return fmt.Errorf("%w: currency must be one of NGN, GHS, KES, ZAR, USD", ErrValidation)
	}
	if req.MonthlyCollectionTarget != nil && *req.MonthlyCollectionTarget < 0 {
		return fmt.Errorf("%w: monthlyCollectionTarget cannot be negative", ErrValidation)
	}
	return nil
}

func validateComplete(req CompleteRequest) error {
	if req.Currency != "" && !supportedCurrencies[strings.ToUpper(req.Currency)] {
		return fmt.Errorf("%w: currency must be one of NGN, GHS, KES, ZAR, USD", ErrValidation)
	}
	// A debt needs somebody to owe it.
	if req.Debt != nil && req.Customer == nil {
		return fmt.Errorf("%w: a debt requires a customer", ErrValidation)
	}
	if req.Customer != nil {
		if strings.TrimSpace(req.Customer.Name) == "" {
			return fmt.Errorf("%w: customer name is required", ErrValidation)
		}
		if e := strings.TrimSpace(req.Customer.Email); e != "" {
			if _, err := mail.ParseAddress(e); err != nil {
				return fmt.Errorf("%w: customer email is not a valid address", ErrValidation)
			}
		}
	}
	if req.Debt != nil {
		if req.Debt.Amount <= 0 {
			return fmt.Errorf("%w: debt amount must be greater than zero", ErrValidation)
		}
		if req.Debt.DueDate == "" {
			return fmt.Errorf("%w: debt dueDate is required", ErrValidation)
		}
		// Checked here so a malformed date is rejected before any transaction
		// opens, rather than surfacing mid-write from the debts package.
		if _, err := time.Parse("2006-01-02", req.Debt.DueDate); err != nil {
			return fmt.Errorf("%w: debt dueDate must be YYYY-MM-DD", ErrValidation)
		}
	}
	return nil
}
