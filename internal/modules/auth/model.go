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
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

// RegisterRequest defines what users send to sign up
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=50"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
}

// LoginRequest defines what users send to log in
type LoginRequest struct {
	Identifier string `json:"identifier" binding:"required"` // Can be Username OR Email
	Password   string `json:"password" binding:"required"`
}