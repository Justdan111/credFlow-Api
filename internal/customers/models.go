package customers

import "time"

type Customer struct {
	ID          string    `json:"id"`
	BusinessID  string    `json:"businessId"`
	Name        string    `json:"name"`
	Email       *string   `json:"email,omitempty"`
	Phone       *string   `json:"phone,omitempty"`
	CompanyName *string   `json:"companyName,omitempty"`
	Address     *string   `json:"address,omitempty"`
	RiskLevel   string    `json:"riskLevel"`
	CreditLimit float64   `json:"creditLimit"`
	Notes       *string   `json:"notes,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type CreateRequest struct {
	Name        string  `json:"name"`
	Email       string  `json:"email"`
	Phone       string  `json:"phone"`
	CompanyName string  `json:"companyName"`
	Address     string  `json:"address"`
	RiskLevel   string  `json:"riskLevel"`
	CreditLimit float64 `json:"creditLimit"`
	Notes       string  `json:"notes"`
}

// UpdateRequest uses pointers so a missing field stays unchanged.
// A nil pointer means "client did not send this field" — leave it alone.
// A non-nil pointer means "client sent this value" — apply it (even if empty).
type UpdateRequest struct {
	Name        *string  `json:"name,omitempty"`
	Email       *string  `json:"email,omitempty"`
	Phone       *string  `json:"phone,omitempty"`
	CompanyName *string  `json:"companyName,omitempty"`
	Address     *string  `json:"address,omitempty"`
	RiskLevel   *string  `json:"riskLevel,omitempty"`
	CreditLimit *float64 `json:"creditLimit,omitempty"`
	Notes       *string  `json:"notes,omitempty"`
}

type ListQuery struct {
	Page      int
	PageSize  int
	Search    string
	RiskLevel string
	Sort      string // API field name; service translates to SQL column
}
