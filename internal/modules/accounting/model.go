package accounting

import (
	"time"
)

// ---------------------------------------------------------
// Database Models (Map directly to SQL Tables)
// ---------------------------------------------------------

type Account struct {
	ID             int64    `json:"id" db:"id"`
	UserID         int64    `json:"user_id" db:"user_id"`
	Name           string   `json:"name" db:"name"`
	Type           string   `json:"type" db:"type"` // cash | bank | e_wallet | credit_card | pay_later
	Balance        float64  `json:"balance" db:"balance"`
	Currency       string   `json:"currency" db:"currency"`
	Icon           *string  `json:"icon" db:"icon"`
	Color          *string  `json:"color" db:"color"`
	IsActive       bool     `json:"is_active" db:"is_active"`
	CreditLimit    *float64 `json:"credit_limit,omitempty" db:"credit_limit"`
	StatementDate  *int     `json:"statement_date,omitempty" db:"statement_date"`
	PaymentDueDate *int     `json:"payment_due_date,omitempty" db:"payment_due_date"`
	MinimumPayment *float64 `json:"minimum_payment,omitempty" db:"minimum_payment"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" db:"updated_at"`
}

type Category struct {
	ID       int64     `json:"id" db:"id"`
	UserID   int64     `json:"user_id" db:"user_id"`
	ParentID *int64    `json:"parent_id" db:"parent_id"`
	Name     string    `json:"name" db:"name"`
	Type     string    `json:"type" db:"type"` // income | expense
	Icon     *string   `json:"icon" db:"icon"`
	Color    *string   `json:"color" db:"color"`
	IsActive bool      `json:"is_active" db:"is_active"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
	Children []Category `json:"children,omitempty"` // For hierarchical response
}

type Tag struct {
	ID        int64     `json:"id" db:"id"`
	UserID    int64     `json:"user_id" db:"user_id"`
	Name      string    `json:"name" db:"name"`
	Color     *string   `json:"color" db:"color"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

type Transaction struct {
	ID                   int64     `json:"id" db:"id"`
	UserID               int64     `json:"user_id" db:"user_id"`
	AccountID            int64     `json:"account_id" db:"account_id"`
	CategoryID           *int64    `json:"category_id" db:"category_id"`
	Amount               float64   `json:"amount" db:"amount"`
	Type                 string    `json:"type" db:"type"` // income | expense | transfer
	TransferToAccountID  *int64    `json:"transfer_to_account_id,omitempty" db:"transfer_to_account_id"`
	ProjectID            *int64    `json:"project_id,omitempty" db:"project_id"`
	RecurringID          *int64    `json:"recurring_id,omitempty" db:"recurring_id"`
	Note                 *string   `json:"note" db:"note"`
	PhotoURL             *string   `json:"photo_url,omitempty" db:"photo_url"`
	Date                 time.Time `json:"date" db:"date"`
	CreatedAt            time.Time `json:"created_at" db:"created_at"`
	UpdatedAt            time.Time `json:"updated_at" db:"updated_at"`
}

// TransactionWithDetails includes related account, category, and tags
type TransactionWithDetails struct {
	Transaction
	Account  *AccountBrief  `json:"account,omitempty"`
	Category *CategoryBrief `json:"category,omitempty"`
	Tags     []Tag          `json:"tags,omitempty"`
}

type AccountBrief struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type CategoryBrief struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ---------------------------------------------------------
// API Request Models
// ---------------------------------------------------------

type CreateAccountRequest struct {
	Name           string   `json:"name" binding:"required"`
	Type           string   `json:"type" binding:"required,oneof=cash bank e_wallet credit_card pay_later"`
	Balance        float64  `json:"balance"`
	Currency       string   `json:"currency"`
	Icon           *string  `json:"icon"`
	Color          *string  `json:"color"`
	CreditLimit    *float64 `json:"credit_limit"`
	StatementDate  *int     `json:"statement_date"`
	PaymentDueDate *int     `json:"payment_due_date"`
	MinimumPayment *float64 `json:"minimum_payment"`
}

type UpdateAccountRequest struct {
	Name           *string  `json:"name"`
	Balance        *float64 `json:"balance"`
	Icon           *string  `json:"icon"`
	Color          *string  `json:"color"`
	IsActive       *bool    `json:"is_active"`
	CreditLimit    *float64 `json:"credit_limit"`
	StatementDate  *int     `json:"statement_date"`
	PaymentDueDate *int     `json:"payment_due_date"`
	MinimumPayment *float64 `json:"minimum_payment"`
}

type CreateCategoryRequest struct {
	Name     string `json:"name" binding:"required"`
	Type     string `json:"type" binding:"required,oneof=income expense"`
	ParentID *int64 `json:"parent_id"`
	Icon     *string `json:"icon"`
	Color    *string `json:"color"`
}

type UpdateCategoryRequest struct {
	Name     *string `json:"name"`
	Icon     *string `json:"icon"`
	Color    *string `json:"color"`
	IsActive *bool   `json:"is_active"`
}

type CreateTagRequest struct {
	Name  string  `json:"name" binding:"required,max=50"`
	Color *string `json:"color"`
}

type UpdateTagRequest struct {
	Name  *string `json:"name"`
	Color *string `json:"color"`
}

type CreateTransactionRequest struct {
	AccountID           int64   `json:"account_id" binding:"required"`
	Type                string  `json:"type" binding:"required,oneof=income expense transfer"`
	Amount              float64 `json:"amount" binding:"required,gt=0"`
	Date                string  `json:"date" binding:"required"` // YYYY-MM-DD
	CategoryID          *int64  `json:"category_id"`
	TransferToAccountID *int64  `json:"transfer_to_account_id"`
	ProjectID           *int64  `json:"project_id"`
	TagIDs              []int64 `json:"tag_ids"`
	Note                *string `json:"note"`
	PhotoURL            *string `json:"photo_url"`
}

type UpdateTransactionRequest struct {
	Amount     *float64 `json:"amount"`
	Date       *string  `json:"date"`
	CategoryID *int64   `json:"category_id"`
	TagIDs     *[]int64 `json:"tag_ids"`
	Note       *string  `json:"note"`
	ProjectID  *int64   `json:"project_id"`
}

type AddTagsRequest struct {
	TagIDs []int64 `json:"tag_ids" binding:"required"`
}

// ---------------------------------------------------------
// Summary / Pagination Models
// ---------------------------------------------------------

type Pagination struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

type TransactionSummary struct {
	TotalIncome  float64           `json:"total_income"`
	TotalExpense float64           `json:"total_expense"`
	Net          float64           `json:"net"`
	ByCategory   []CategorySummary `json:"by_category,omitempty"`
}

type CategorySummary struct {
	CategoryID int64   `json:"category_id"`
	Name       string  `json:"name"`
	Total      float64 `json:"total"`
}

type AccountSummary struct {
	AccountID        int64   `json:"account_id"`
	Balance          float64 `json:"balance"`
	TotalIncome      float64 `json:"total_income"`
	TotalExpense     float64 `json:"total_expense"`
	TransactionCount int     `json:"transaction_count"`
}
