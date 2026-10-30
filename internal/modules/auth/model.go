package auth

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type User struct {
	ID              uuid.UUID     `json:"id"`
	Username        string        `json:"username"`
	Email           *string       `json:"email"`
	EmailVerifiedAt *time.Time    `json:"email_verified_at"`
	DisplayName     string        `json:"display_name"`
	PasswordHash    string        `json:"-"` // "" = no password (an account made through an outside app)
	Currency        string        `json:"currency"`
	Avatar          shared.Avatar `json:"-"`
	CreatedVia      string        `json:"-"` // insert only: password | telegram | line | google
	Status          string        `json:"status"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
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

// ChangePasswordRequest — current_password is required only when the account has a password
// (0.4.2 1b-2: a passwordless account sets its first one with new_password alone).
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
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

type AuthUser struct {
	ID              uuid.UUID     `json:"id"`
	Username        string        `json:"username"`
	Email           *string       `json:"email"`
	EmailVerifiedAt *time.Time    `json:"email_verified_at"`
	DisplayName     string        `json:"display_name"`
	Currency        string        `json:"currency"`
	Avatar          shared.Avatar `json:"avatar"`
	Status          string        `json:"status"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

func APIStatus(stored string) string {
	if stored == "active" {
		return "active"
	}
	return "deactivated"
}

func (u *User) AuthView() AuthUser {
	return AuthUser{
		ID: u.ID, Username: u.Username, Email: u.Email, EmailVerifiedAt: u.EmailVerifiedAt,
		DisplayName: u.DisplayName, Currency: u.Currency, Avatar: u.Avatar,
		Status: APIStatus(u.Status), CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}
