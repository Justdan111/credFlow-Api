package businesses

import "time"

// Business is the full profile view. internal/auth keeps its own smaller
// Business for the auth response: two views of one table is deliberate, so the
// login payload does not grow every profile field added here.
type Business struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Industry *string `json:"industry"`
	Size     *string `json:"size"`

	Currency                string   `json:"currency"`
	MonthlyCollectionTarget *float64 `json:"monthlyCollectionTarget"`

	// CurrencyLocked is computed, never stored. The frontend needs it to
	// disable the currency selector rather than offer a change the API will
	// reject with 409.
	CurrencyLocked bool `json:"currencyLocked"`

	OnboardingCompleted   bool       `json:"onboardingCompleted"`
	OnboardingCompletedAt *time.Time `json:"onboardingCompletedAt"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// UpdateRequest uses pointers so an omitted key is left unchanged, while an
// explicit null clears MonthlyCollectionTarget.
type UpdateRequest struct {
	Name                    *string  `json:"name,omitempty"`
	Industry                *string  `json:"industry,omitempty"`
	Size                    *string  `json:"size,omitempty"`
	Currency                *string  `json:"currency,omitempty"`
	MonthlyCollectionTarget *float64 `json:"monthlyCollectionTarget,omitempty"`
}

// OnboardingStatus reports progress. The per-step flags are derived from real
// records rather than a stored counter, so they cannot drift out of sync with
// what the business actually has.
type OnboardingStatus struct {
	Completed   bool   `json:"completed"`
	CurrentStep string `json:"currentStep"`
	Steps       struct {
		Business bool `json:"business"`
		Customer bool `json:"customer"`
		Debt     bool `json:"debt"`
	} `json:"steps"`
}

// CompleteRequest carries the whole three-step onboarding payload. Customer and
// debt are optional — a user may skip them — but a debt without a customer is
// rejected, since there would be nobody to owe it.
type CompleteRequest struct {
	Industry string `json:"industry"`
	Size     string `json:"size"`
	Currency string `json:"currency"`
	Customer *struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Phone string `json:"phone"`
	} `json:"customer,omitempty"`
	Debt *struct {
		Amount  float64 `json:"amount"`
		DueDate string  `json:"dueDate"` // YYYY-MM-DD
	} `json:"debt,omitempty"`
}

// CompleteResponse tells the frontend what was actually created.
type CompleteResponse struct {
	Business   Business `json:"business"`
	CustomerID *string  `json:"customerId"`
	DebtID     *string  `json:"debtId"`
}
