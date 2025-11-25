package accounting

import (
	"time"
)

// ---------------------------------------------------------
// Database Models (Map directly to SQL Tables)
// ---------------------------------------------------------

type Account struct {
	ID             int64     `json:"id" db:"id"`
	UserID         int64     `json:"user_id" db:"user_id"`
	Name           string    `json:"name" db:"name"`
	Type           string    `json:"type" db:"type"` // bank, cash, credit
	Currency       string    `json:"currency" db:"currency"`
	InitialBalance float64   `json:"initial_balance" db:"initial_balance"`
	CurrentBalance float64   `json:"current_balance" db:"current_balance"`
	Color          string    `json:"color" db:"color"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" db:"updated_at"`
}

type Category struct {
	ID        int64     `json:"id" db:"id"`
	UserID    int64     `json:"user_id" db:"user_id"`
	ParentID  *int64    `json:"parent_id" db:"parent_id"` // Pointer because it can be NULL
	Name      string    `json:"name" db:"name"`
	Type      string    `json:"type" db:"type"` // income, expense
	Icon      string    `json:"icon" db:"icon"`
	Color     string    `json:"color" db:"color"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

type Transaction struct {
	ID                  int64     `json:"id" db:"id"`
	UserID              int64     `json:"user_id" db:"user_id"`
	AccountID           int64     `json:"account_id" db:"account_id"`
	CategoryID          *int64    `json:"category_id" db:"category_id"` // Can be NULL for transfers
	Amount              float64   `json:"amount" db:"amount"`
	Type                string    `json:"type" db:"type"` // income, expense, transfer
	Description         string    `json:"description" db:"description"`
	TransactionDate     time.Time `json:"transaction_date" db:"transaction_date"`
	LinkedTransactionID *int64    `json:"linked_transaction_id" db:"linked_transaction_id"`
	CreatedAt           time.Time `json:"created_at" db:"created_at"`
	UpdatedAt           time.Time `json:"updated_at" db:"updated_at"`
}

// ---------------------------------------------------------
// API Request Models (Input from Frontend)
// ---------------------------------------------------------

type CreateAccountRequest struct {
	Name           string  `json:"name" binding:"required"`
	Type           string  `json:"type" binding:"required,oneof=bank cash credit"`
	Currency       string  `json:"currency" binding:"required,len=3"`
	InitialBalance float64 `json:"initial_balance"`
	Color          string  `json:"color"`
}

type CreateCategoryRequest struct {
	Name     string `json:"name" binding:"required"`
	Type     string `json:"type" binding:"required,oneof=income expense"`
	ParentID *int64 `json:"parent_id"` // Optional
	Icon     string `json:"icon"`
	Color    string `json:"color"`
}

type CreateTransactionRequest struct {
	AccountID       int64   `json:"account_id" binding:"required"`
	CategoryID      *int64  `json:"category_id"` // Required for income/expense
	Amount          float64 `json:"amount" binding:"required,gt=0"`
	Type            string  `json:"type" binding:"required,oneof=income expense transfer"`
	Description     string  `json:"description"`
	TransactionDate string  `json:"transaction_date" binding:"required"` // Format: "2025-11-25T10:00:00Z"
	
	// For Transfers only
	TargetAccountID *int64 `json:"target_account_id"` 
}