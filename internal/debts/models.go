package debts

import "time"

type Debt struct {
	ID              string     `json:"id"`
	BusinessID      string     `json:"businessId"`
	CustomerID      string     `json:"customerId"`
	Amount          float64    `json:"amount"`
	AmountPaid      float64    `json:"amountPaid"`      // SUM of active payments against this debt
	AmountRemaining float64    `json:"amountRemaining"` // derived: see repository
	Description     *string    `json:"description,omitempty"`
	Status          string     `json:"status"`
	Overdue         bool       `json:"overdue"`
	IssuedDate      time.Time  `json:"issuedDate"`
	DueDate         time.Time  `json:"dueDate"`
	PaidAt          *time.Time `json:"paidAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type CreateRequest struct {
	CustomerID  string  `json:"customerId"`
	Amount      float64 `json:"amount"`
	Description string  `json:"description"`
	IssuedDate  string  `json:"issuedDate"` // "YYYY-MM-DD"; empty = today
	DueDate     string  `json:"dueDate"`    // "YYYY-MM-DD"; required
}

// UpdateRequest uses pointers: a nil field is left unchanged.
type UpdateRequest struct {
	Amount      *float64 `json:"amount,omitempty"`
	Description *string  `json:"description,omitempty"`
	DueDate     *string  `json:"dueDate,omitempty"`
}

type ListQuery struct {
	Page       int
	PageSize   int
	Status     string
	CustomerID string
	Overdue    string // "true" filters to overdue only; "" = no filter
	Sort       string
}
