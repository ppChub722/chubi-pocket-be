package auth

import (
	"time"

	"github.com/google/uuid"
)

// User is the canonical identity object. Owned by the auth module per
// design/spec/01-auth.md; the users module reads/writes the editable subset.
type User struct {
	ID              uuid.UUID  `json:"id"`
	Username        string     `json:"username"`
	Email           *string    `json:"email"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
	DisplayName     string     `json:"display_name"`
	PasswordHash    string     `json:"-"`
	Currency        string     `json:"currency"`
	AvatarURL       *string    `json:"avatar_url"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type RegisterRequest struct {
	Username    string  `json:"username"     binding:"required,min=3,max=50"`
	Password    string  `json:"password"     binding:"required,min=8,max=128"`
	DisplayName string  `json:"display_name" binding:"required,min=1,max=100"`
	Email       *string `json:"email"        binding:"omitempty,email,max=255"`
	Currency    string  `json:"currency"     binding:"omitempty,len=3"`
}

type LoginRequest struct {
	Identifier string `json:"identifier" binding:"required"`
	Password   string `json:"password"   binding:"required"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password"     binding:"required,min=8,max=128"`
}

type AuthResponse struct {
	User  User      `json:"user"`
	Token TokenInfo `json:"token"`
}

type TokenInfo struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}
