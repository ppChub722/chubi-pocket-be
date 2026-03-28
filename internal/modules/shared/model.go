package shared

import "time"

// =============================================================
// Database Models
// =============================================================

type SharedExpense struct {
	ID            int64     `json:"id"`
	TransactionID int64     `json:"transaction_id"`
	TotalAmount   float64   `json:"total_amount"`
	Description   *string   `json:"description"`
	CreatedAt     time.Time `json:"created_at"`
}

type SharedExpenseWithSplits struct {
	SharedExpense
	Splits           []Split `json:"splits"`
	TotalSettled     float64 `json:"total_settled"`
	TotalOutstanding float64 `json:"total_outstanding"`
}

type SharedExpenseListItem struct {
	SharedExpense
	SplitsCount    int `json:"splits_count"`
	UnsettledCount int `json:"unsettled_count"`
}

type Split struct {
	ID              int64     `json:"id"`
	SharedExpenseID int64     `json:"shared_expense_id"`
	PersonName      string    `json:"person_name"`
	OwedAmount      float64   `json:"owed_amount"`
	PaidAmount      float64   `json:"paid_amount"`
	IsSettled       bool      `json:"is_settled"`
	Outstanding     float64   `json:"outstanding"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Settlement struct {
	ID          int64     `json:"id"`
	SplitID     int64     `json:"split_id"`
	Amount      float64   `json:"amount"`
	SettledDate string    `json:"settled_date"`
	Note        *string   `json:"note"`
	CreatedAt   time.Time `json:"created_at"`
}

type SharedExpenseSummary struct {
	TotalOwedToMe    float64 `json:"total_owed_to_me"`
	TotalIOwe        float64 `json:"total_i_owe"`
	OpenSplitsCount  int     `json:"open_splits_count"`
	SettledSplitsCount int   `json:"settled_splits_count"`
}

// =============================================================
// Request Models
// =============================================================

type CreateSharedExpenseRequest struct {
	TransactionID int64              `json:"transaction_id" binding:"required"`
	TotalAmount   float64            `json:"total_amount" binding:"required,gt=0"`
	Description   *string            `json:"description"`
	Splits        []CreateSplitInput `json:"splits" binding:"required,min=1"`
}

type CreateSplitInput struct {
	PersonName string  `json:"person_name" binding:"required"`
	OwedAmount float64 `json:"owed_amount" binding:"required,gt=0"`
}

type UpdateSharedExpenseRequest struct {
	Description *string `json:"description"`
}

type CreateSplitRequest struct {
	PersonName string  `json:"person_name" binding:"required"`
	OwedAmount float64 `json:"owed_amount" binding:"required,gt=0"`
}

type UpdateSplitRequest struct {
	PersonName *string  `json:"person_name"`
	OwedAmount *float64 `json:"owed_amount"`
}

type CreateSettlementRequest struct {
	Amount      float64 `json:"amount" binding:"required,gt=0"`
	SettledDate string  `json:"settled_date" binding:"required"`
	Note        *string `json:"note"`
}
