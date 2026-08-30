package auth

import "time"

type User struct {
	ID           string    `json:"id"`
	BusinessID   string    `json:"businessId"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	Role         string    `json:"role"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Business struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Industry  *string   `json:"industry,omitempty"`
	Size      *string   `json:"size,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type RegisterRequest struct {
	BusinessName string `json:"businessName"`
	Industry     string `json:"industry"`
	Size         string `json:"size"`
	Email        string `json:"email"`
	Password     string `json:"password"`
	Name         string `json:"name"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthResponse struct {
	User     User     `json:"user"`
	Business Business `json:"business"`
	// AccessToken is short-lived (15m by default). The long-lived refresh
	// token is never in the body — it travels only in an httpOnly cookie so
	// that JavaScript, and therefore XSS, can never read it.
	AccessToken string `json:"accessToken,omitempty"`
}

// RefreshToken is one link in a rotation chain. The opaque token itself is
// never stored or held here — only its SHA-256 digest reaches the database.
type RefreshToken struct {
	ID         string
	UserID     string
	BusinessID string
	// FamilyID groups every token descended from a single login.
	FamilyID string
	// ExpiresAt is this link's own expiry; AbsoluteExpiresAt is the family's
	// hard deadline, copied forward unchanged so rotation cannot extend it.
	ExpiresAt         time.Time
	AbsoluteExpiresAt time.Time
	// UsedAt non-nil means retired by rotation; RevokedAt non-nil means killed.
	UsedAt    *time.Time
	RevokedAt *time.Time
}
