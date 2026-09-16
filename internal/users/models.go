package users

import "time"

// Member is one person on a business's team. It deliberately carries no
// password hash: this type is serialised straight to the client.
type Member struct {
	ID         string  `json:"id"`
	BusinessID string  `json:"businessId"`
	Email      string  `json:"email"`
	Name       string  `json:"name"`
	Phone      string  `json:"phone"`
	Role       string  `json:"role"`
	InvitedBy  *string `json:"invitedBy"`
	// Null until they first sign in, which is how a pending invite is detected.
	LastActiveAt *time.Time `json:"lastActiveAt"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

type InviteRequest struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// InviteResponse carries no token or link: the invitation is a credential for
// taking over an account, so it goes only to the invitee's inbox.
type InviteResponse struct {
	Member Member `json:"member"`
}

// UpdateRequest uses pointers so an omitted key is left unchanged. Email is
// absent: a login identifier needs its own verification flow.
type UpdateRequest struct {
	Name *string `json:"name,omitempty"`
	Role *string `json:"role,omitempty"`
}

type ListQuery struct {
	Page     int
	PageSize int
	Role     string
}
