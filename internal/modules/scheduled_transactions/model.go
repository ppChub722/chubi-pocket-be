package scheduled_transactions

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Type values.
const (
	TypeExpense = "expense"
	TypeIncome  = "income"
)

// Entry-type values.
const (
	EntryRecurring   = "recurring"
	EntryInstallment = "installment"
)

// Billing-cycle values.
const (
	CycleDaily   = "daily"
	CycleWeekly  = "weekly"
	CycleMonthly = "monthly"
	CycleYearly  = "yearly"
)

// Status values.
const (
	StatusActive    = "active"
	StatusPaused    = "paused"
	StatusCancelled = "cancelled"
	StatusCompleted = "completed"
)

// ScheduledTransaction — stored row. Installment-only fields are
// pointer-typed so we can faithfully round-trip NULL.
type ScheduledTransaction struct {
	ID                    uuid.UUID        `json:"id"`
	UserID                uuid.UUID        `json:"user_id"`
	AccountID             uuid.UUID        `json:"account_id"`
	Name                  string           `json:"name"`
	Type                  string           `json:"type"`
	EntryType             string           `json:"entry_type"`
	Amount                float64          `json:"amount"`
	CategoryID            *uuid.UUID       `json:"category_id"`
	BillingCycle          string           `json:"billing_cycle"`
	NextBillingDate       string           `json:"next_billing_date"` // YYYY-MM-DD
	DayOfMonth            int              `json:"day_of_month"`
	Status                string           `json:"status"`
	Note                  *string          `json:"note"`
	TotalAmount           *float64         `json:"total_amount"`
	DownPayment           *float64         `json:"down_payment"`
	TotalInstallments     *int             `json:"total_installments"`
	RemainingInstallments *int             `json:"remaining_installments"`
	InterestRate          *float64         `json:"interest_rate"`
	IconCode              *shared.IconCode `json:"icon_code"`
	LogoURL               *string          `json:"logo_url"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

// ScheduledTransactionView extends the row with embedded account /
// category snapshots so the FE list/detail can render directly.
type AccountRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type CategoryRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type ScheduledTransactionView struct {
	ScheduledTransaction
	Account  *AccountRef  `json:"account,omitempty"`
	Category *CategoryRef `json:"category,omitempty"`
}

// CreateRequest — `entry_type='recurring'` rejects installment-only
// fields; `installment` requires `total_amount` and `total_installments`
// (and `remaining_installments` defaulting to total). Both flavors share
// the rest of the schema.
//
// `category_id` is required: generated transactions render their icon
// from the category, mirroring how regular transactions display.
type CreateRequest struct {
	Name                  string           `json:"name"               binding:"required,min=1,max=100"`
	Type                  string           `json:"type"               binding:"required,oneof=expense income"`
	EntryType             string           `json:"entry_type"         binding:"required,oneof=recurring installment"`
	Amount                float64          `json:"amount"             binding:"required,gt=0"`
	AccountID             uuid.UUID        `json:"account_id"         binding:"required"`
	CategoryID            uuid.UUID        `json:"category_id"        binding:"required"`
	BillingCycle          string           `json:"billing_cycle"      binding:"required,oneof=daily weekly monthly yearly"`
	NextBillingDate       string           `json:"next_billing_date"  binding:"required,datetime=2006-01-02"`
	Note                  *string          `json:"note"`
	TotalAmount           *float64         `json:"total_amount"       binding:"omitempty,gt=0"`
	DownPayment           *float64         `json:"down_payment"       binding:"omitempty,gte=0"`
	TotalInstallments     *int             `json:"total_installments" binding:"omitempty,gt=0"`
	RemainingInstallments *int             `json:"remaining_installments" binding:"omitempty,gte=0"`
	InterestRate          *float64         `json:"interest_rate"      binding:"omitempty,gte=0,lte=99.99"`
	IconCode              *shared.IconCode `json:"icon_code"`
	LogoURL               *string          `json:"logo_url"`
}

// UpdateRequest — partial. `type`, `entry_type`, `status` are NOT
// editable (status flows via pause/resume/cancel endpoints).
type UpdateRequest struct {
	Name                  *string          `json:"name"               binding:"omitempty,min=1,max=100"`
	Amount                *float64         `json:"amount"             binding:"omitempty,gt=0"`
	AccountID             *uuid.UUID       `json:"account_id"         binding:"omitempty"`
	CategoryID            *uuid.UUID       `json:"category_id"        binding:"omitempty"`
	BillingCycle          *string          `json:"billing_cycle"      binding:"omitempty,oneof=daily weekly monthly yearly"`
	NextBillingDate       *string          `json:"next_billing_date"  binding:"omitempty,datetime=2006-01-02"`
	Note                  *string          `json:"note"`
	TotalAmount           *float64         `json:"total_amount"       binding:"omitempty,gt=0"`
	DownPayment           *float64         `json:"down_payment"       binding:"omitempty,gte=0"`
	TotalInstallments     *int             `json:"total_installments" binding:"omitempty,gt=0"`
	RemainingInstallments *int             `json:"remaining_installments" binding:"omitempty,gte=0"`
	InterestRate          *float64         `json:"interest_rate"      binding:"omitempty,gte=0,lte=99.99"`
	IconCode              *shared.IconCode `json:"icon_code"`
	LogoURL               *string          `json:"logo_url"`
}

type ListFilter struct {
	Status    string // 'active' (default), 'paused', 'cancelled', 'completed', 'all'
	EntryType string // 'recurring', 'installment', 'all'
	Type      string // 'expense', 'income', 'all'
	AccountID *uuid.UUID
}

type ListResponse struct {
	Data []ScheduledTransactionView `json:"data"`
}

// UpcomingItem is one row of the /upcoming endpoint.
type UpcomingItem struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	Type            string    `json:"type"`
	NextBillingDate string    `json:"next_billing_date"`
	Amount          float64   `json:"amount"`
	DaysUntil       int       `json:"days_until"`
}

type UpcomingResponse struct {
	Data            []UpcomingItem `json:"data"`
	TotalExpenseDue float64        `json:"total_expense_due"`
	TotalIncomeDue  float64        `json:"total_income_due"`
}

// GenerateNowResponse is returned by POST :id/generate-now.
type GeneratedTransaction struct {
	ID     uuid.UUID `json:"id"`
	Amount float64   `json:"amount"`
	Date   string    `json:"date"`
}

type ScheduleUpdated struct {
	NextBillingDate       string  `json:"next_billing_date"`
	RemainingInstallments *int    `json:"remaining_installments"`
	Status                string  `json:"status"`
}

type GenerateNowResponse struct {
	GeneratedTransaction GeneratedTransaction `json:"generated_transaction"`
	ScheduleUpdated      ScheduleUpdated      `json:"schedule_updated"`
}

// HistoryItem and HistoryResponse back GET :id/history.
type HistoryItem struct {
	ID          uuid.UUID `json:"id"`
	Amount      float64   `json:"amount"`
	Date        string    `json:"date"`
	GeneratedAt time.Time `json:"generated_at"`
}

type HistoryResponse struct {
	Data                 []HistoryItem `json:"data"`
	TotalGenerated       int           `json:"total_generated"`
	TotalAmountGenerated float64       `json:"total_amount_generated"`
}
