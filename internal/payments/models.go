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
	DebtID         string  `json:"debtId"` // empty = unattributed
	Amount         float64 `json:"amount"`
	Method         string  `json:"method"` // empty -> "cash"
	Reference      string  `json:"reference"`
	Notes          string  `json:"notes"`
	PaidAt         string  `json:"paidAt"`         // RFC3339; empty -> now()
	IdempotencyKey string  `json:"idempotencyKey"` // empty = no guard
}

// UpdateRequest corrects a recorded payment. Pointers, so an omitted key is
// left unchanged.
//
// CustomerID and DebtID are absent on purpose. Re-pointing a payment at a
// different debt would silently change two balances at once, and the honest way
// to express that is to void the payment and record it again — which leaves
// both actions in the audit trail instead of one opaque edit.
type UpdateRequest struct {
	Amount    *float64 `json:"amount,omitempty"`
	Method    *string  `json:"method,omitempty"`
	Reference *string  `json:"reference,omitempty"`
	Notes     *string  `json:"notes,omitempty"`
	PaidAt    *string  `json:"paidAt,omitempty"` // RFC3339
}

type ListQuery struct {
	Page       int
	PageSize   int
	CustomerID string
	DebtID     string
	Method     string
	Sort       string
}
