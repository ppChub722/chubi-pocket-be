package auth

import (
	"time"
)

// User matches the 'users' table in Postgres
type User struct {
	ID           int64     `json:"id" db:"id"`
	Username     string    `json:"username" db:"username"`
	Email        string    `json:"email" db:"email"`
	PasswordHash string    `json:"-" db:"password_hash"` // json:"-" means never send this to the user
	Currency     string    `json:"currency" db:"currency"`
	AvatarURL    *string   `json:"avatar_url" db:"avatar_url"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

// RegisterRequest defines what users send to sign up
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=50"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
	Currency string `json:"currency"`
}

// LoginRequest defines what users send to log in
type LoginRequest struct {
	Identifier string `json:"identifier" binding:"required"` // Can be Username OR Email
	Password   string `json:"password" binding:"required"`
}

// UpdateProfileRequest defines fields for PUT /users/me
type UpdateProfileRequest struct {
	Name      *string `json:"name"`
	Currency  *string `json:"currency"`
	AvatarURL *string `json:"avatar_url"`
}

// ChangePasswordRequest defines fields for PUT /users/me/password
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required,min=8"`
}
