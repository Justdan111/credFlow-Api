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
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Industry *string `json:"industry,omitempty"`
	Size     *string `json:"size,omitempty"`
	// Currency and OnboardingCompleted let the frontend render amounts and
	// decide between the dashboard and the onboarding flow straight from the
	// login response, instead of a second round-trip that would briefly show
	// the wrong screen. Additive fields — no existing key changes.
	Currency            string `json:"currency"`
	OnboardingCompleted bool   `json:"onboardingCompleted"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Profile is the /api/auth/me view. Separate from User so phone can be
// returned without widening the type every other package scans into.
type Profile struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// UpdateProfileRequest uses pointers so an omitted key is left unchanged.
// Email is absent on purpose: changing a login identifier needs its own
// verification flow, and allowing it here would let a stolen access token lock
// the real owner out.
type UpdateProfileRequest struct {
	Name  *string `json:"name,omitempty"`
	Phone *string `json:"phone,omitempty"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type ForgotPasswordRequest struct {
	Email string `json:"email"`
}

type ResetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"newPassword"`
}

// SessionView is one active login as the settings screen shows it.
type SessionView struct {
	ID           string    `json:"id"`
	UserAgent    string    `json:"userAgent"`
	CreatedAt    time.Time `json:"createdAt"`
	LastActiveAt time.Time `json:"lastActiveAt"`
	// Current marks the session making this request, so the UI can label it
	// and warn before revoking it.
	Current bool `json:"current"`
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
