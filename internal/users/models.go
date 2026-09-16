package users

import "time"

// Member is one person on a business's team.
//
// No password hash, ever: this type is serialised straight to the client, and
// a field that must never be exposed is best not present on the struct that
// gets marshalled.
type Member struct {
	ID         string     `json:"id"`
	BusinessID string     `json:"businessId"`
	Email      string     `json:"email"`
	Name       string     `json:"name"`
	Phone      string     `json:"phone"`
	Role       string     `json:"role"`
	InvitedBy  *string    `json:"invitedBy"`
	// LastActiveAt is null for someone who has never signed in — which is how
	// the UI can show an invitation as still outstanding without a separate
	// invitations table to keep in sync.
	LastActiveAt *time.Time `json:"lastActiveAt"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

type InviteRequest struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// InviteResponse returns the created member.
//
// It deliberately carries no token or link. The invitation is a credential for
// taking over an account, so it goes to the invitee's inbox and nowhere else —
// returning it here would let anyone who can invite also read the link and
// claim the account themselves.
type InviteResponse struct {
	Member Member `json:"member"`
}

// UpdateRequest uses pointers so an omitted key is left unchanged.
// Email is absent for the same reason it is absent from PATCH /auth/me: a login
// identifier needs its own verification flow.
type UpdateRequest struct {
	Name *string `json:"name,omitempty"`
	Role *string `json:"role,omitempty"`
}

type ListQuery struct {
	Page     int
	PageSize int
	Role     string
}
