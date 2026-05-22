package payments

import "time"

type Payment struct {
	ID             string    `json:"id"`
	BusinessID     string    `json:"businessId"`
	CustomerID     string    `json:"customerId"`
	DebtID         *string   `json:"debtId,omitempty"`
	Amount         float64   `json:"amount"`
	Method         string    `json:"method"`
	Reference      *string   `json:"reference,omitempty"`
	Notes          *string   `json:"notes,omitempty"`
	PaidAt         time.Time `json:"paidAt"`
	IdempotencyKey *string   `json:"idempotencyKey,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type CreateRequest struct {
	CustomerID     string  `json:"customerId"`
	DebtID         string  `json:"debtId"`         // empty = unattributed
	Amount         float64 `json:"amount"`
	Method         string  `json:"method"`         // empty -> "cash"
	Reference      string  `json:"reference"`
	Notes          string  `json:"notes"`
	PaidAt         string  `json:"paidAt"`         // RFC3339; empty -> now()
	IdempotencyKey string  `json:"idempotencyKey"` // empty = no guard
}

type ListQuery struct {
	Page       int
	PageSize   int
	CustomerID string
	DebtID     string
	Method     string
	Sort       string
}
