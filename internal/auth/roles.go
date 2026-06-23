package auth

// Role names. Mirror migration 0001's CHECK constraint on users.role.
// Keep these strings in sync with the DB CHECK; the DB is the source of truth.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)
