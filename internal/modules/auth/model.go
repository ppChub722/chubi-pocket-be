package auth

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// User is the canonical identity object. Owned by the auth module per
// design/spec/01-auth.md; the users module reads/writes the editable subset.
type User struct {
	ID              uuid.UUID        `json:"id"`
	Username        string           `json:"username"`
	Email           *string          `json:"email"`
	EmailVerifiedAt *time.Time       `json:"email_verified_at"`
	DisplayName     string           `json:"display_name"`
	PasswordHash    string           `json:"-"`
	Currency        string           `json:"currency"`
	IconCode        *shared.IconCode `json:"icon_code"`
	Status          string           `json:"status"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
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
	User  AuthUser  `json:"user"`
	Token TokenInfo `json:"token"`
}

type TokenInfo struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// AuthUser is the spec schema AuthUser (the user inside a register / login
// response). Status is the API enum: active | deactivated.
type AuthUser struct {
	ID              uuid.UUID        `json:"id"`
	Username        string           `json:"username"`
	Email           *string          `json:"email"`
	EmailVerifiedAt *time.Time       `json:"email_verified_at"`
	DisplayName     string           `json:"display_name"`
	Currency        string           `json:"currency"`
	IconCode        *shared.IconCode `json:"icon_code"`
	Status          string           `json:"status"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

// APIStatus maps the stored users.status to the API enum.
func APIStatus(stored string) string {
	if stored == "active" {
		return "active"
	}
	return "deactivated"
}

// AuthView is the API shape of the user.
func (u *User) AuthView() AuthUser {
	return AuthUser{
		ID: u.ID, Username: u.Username, Email: u.Email, EmailVerifiedAt: u.EmailVerifiedAt,
		DisplayName: u.DisplayName, Currency: u.Currency, IconCode: u.IconCode,
		Status: APIStatus(u.Status), CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}
