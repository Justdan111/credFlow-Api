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
	Token    string   `json:"token"`
}
