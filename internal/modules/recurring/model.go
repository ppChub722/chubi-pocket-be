package recurring

import "time"

type Recurring struct {
	ID                    int64    `json:"id"`
	UserID                int64    `json:"user_id"`
	AccountID             int64    `json:"account_id"`
	Name                  string   `json:"name"`
	Type                  string   `json:"type"`       // expense | income
	EntryType             string   `json:"entry_type"` // recurring | installment
	Amount                float64  `json:"amount"`
	CategoryID            *int64   `json:"category_id"`
	BillingCycle          string   `json:"billing_cycle"` // daily | weekly | monthly | yearly
	NextBillingDate       string   `json:"next_billing_date"`
	Status                string   `json:"status"` // active | paused | cancelled | completed
	TotalAmount           *float64 `json:"total_amount,omitempty"`
	DownPayment           *float64 `json:"down_payment,omitempty"`
	MonthlyPayment        *float64 `json:"monthly_payment,omitempty"`
	TotalInstallments     *int     `json:"total_installments,omitempty"`
	RemainingInstallments *int     `json:"remaining_installments,omitempty"`
	Note                  *string  `json:"note"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type RecurringListItem struct {
	Recurring
	Category *CategoryBrief `json:"category,omitempty"`
	Account  *AccountBrief  `json:"account,omitempty"`
	ProgressPct *int        `json:"progress_pct,omitempty"`
}

type CategoryBrief struct {
	Name string `json:"name"`
}

type AccountBrief struct {
	Name string `json:"name"`
}

type UpcomingItem struct {
	Recurring
	DaysUntil int            `json:"days_until"`
	Account   *AccountBrief  `json:"account,omitempty"`
}

// =============================================================
// Request Models
// =============================================================

type CreateRecurringRequest struct {
	Name              string   `json:"name" binding:"required"`
	AccountID         int64    `json:"account_id" binding:"required"`
	Type              string   `json:"type" binding:"required,oneof=expense income"`
	EntryType         string   `json:"entry_type" binding:"required,oneof=recurring installment"`
	Amount            float64  `json:"amount" binding:"required,gt=0"`
	BillingCycle      string   `json:"billing_cycle" binding:"required,oneof=daily weekly monthly yearly"`
	NextBillingDate   string   `json:"next_billing_date" binding:"required"`
	CategoryID        *int64   `json:"category_id"`
	Note              *string  `json:"note"`
	TotalAmount       *float64 `json:"total_amount"`
	DownPayment       *float64 `json:"down_payment"`
	MonthlyPayment    *float64 `json:"monthly_payment"`
	TotalInstallments *int     `json:"total_installments"`
}

type UpdateRecurringRequest struct {
	Name            *string  `json:"name"`
	Amount          *float64 `json:"amount"`
	CategoryID      *int64   `json:"category_id"`
	NextBillingDate *string  `json:"next_billing_date"`
	Note            *string  `json:"note"`
}
