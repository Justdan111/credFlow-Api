package audit

import "time"

// Action names, dotted "<entity>.<verb>". Constants rather than free strings: a
// typo stays invisible until somebody needs the entry it misfiled.
const (
	ActionCustomerDeleted = "customer.deleted"

	ActionDebtUpdated    = "debt.updated"
	ActionDebtDeleted    = "debt.deleted"
	ActionDebtMarkedPaid = "debt.marked_paid"

	ActionPaymentCreated = "payment.created"
	ActionPaymentUpdated = "payment.updated"
	ActionPaymentVoided  = "payment.voided"

	ActionBusinessUpdated = "business.updated"

	ActionUserInvited     = "user.invited"
	ActionUserRoleChanged = "user.role_changed"
	ActionUserRemoved     = "user.removed"

	ActionPasswordChanged = "auth.password_changed"
)

const (
	EntityCustomer = "customer"
	EntityDebt     = "debt"
	EntityPayment  = "payment"
	EntityBusiness = "business"
	EntityUser     = "user"
)

// Entry is one recorded action.
type Entry struct {
	ID         string `json:"id"`
	BusinessID string `json:"businessId"`

	// Null once that user is removed; the denormalised name and email remain.
	ActorID    *string `json:"actorId"`
	ActorEmail string  `json:"actorEmail"`
	ActorName  string  `json:"actorName"`

	Action     string  `json:"action"`
	EntityType string  `json:"entityType"`
	EntityID   *string `json:"entityId"`

	// Whatever makes the entry meaningful later: an amount, a granted role.
	Metadata map[string]any `json:"metadata"`

	IP        *string   `json:"ip"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListQuery filters the trail.
type ListQuery struct {
	Page       int
	PageSize   int
	Action     string
	EntityType string
	EntityID   string
	ActorID    string
}
