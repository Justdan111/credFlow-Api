package audit

import "time"

// Action names. Dotted "<entity>.<verb>", so a client can filter by prefix and
// a reader can scan the column without a legend.
//
// Constants rather than free strings: a typo in an audit action is invisible
// until somebody investigates an incident and the entry they need is filed
// under "payement.voided".
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

	// ActorID is null once that user is removed; the denormalised email and
	// name keep the entry readable regardless.
	ActorID    *string `json:"actorId"`
	ActorEmail string  `json:"actorEmail"`
	ActorName  string  `json:"actorName"`

	Action     string  `json:"action"`
	EntityType string  `json:"entityType"`
	EntityID   *string `json:"entityId"`

	// Metadata carries whatever makes the entry meaningful a year later: the
	// amount voided, the role somebody was promoted to.
	Metadata map[string]any `json:"metadata"`

	IP        *string   `json:"ip"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListQuery filters the trail. Deliberately narrow: an audit screen is read
// chronologically, and the two useful cuts are "what did this action do" and
// "what happened to this row".
type ListQuery struct {
	Page       int
	PageSize   int
	Action     string
	EntityType string
	EntityID   string
	ActorID    string
}
