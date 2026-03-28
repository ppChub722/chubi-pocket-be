package budget

import "time"

// =============================================================
// Database Models
// =============================================================

type Budget struct {
	ID         int64     `json:"id"`
	UserID     int64     `json:"user_id"`
	CategoryID int64     `json:"category_id"`
	Amount     float64   `json:"amount"`
	Period     string    `json:"period"` // weekly | monthly | yearly
	IsActive   bool      `json:"is_active"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type BudgetWithSpending struct {
	Budget
	Category        CategoryBrief `json:"category"`
	SpentThisPeriod float64       `json:"spent_this_period"`
	Remaining       float64       `json:"remaining"`
	UtilizationPct  int           `json:"utilization_pct"`
}

type BudgetDetail struct {
	BudgetWithSpending
	CurrentPeriodStart string          `json:"current_period_start"`
	CurrentPeriodEnd   string          `json:"current_period_end"`
	ChildBreakdown     []ChildSpending `json:"child_breakdown,omitempty"`
}

type ChildSpending struct {
	Category string  `json:"category"`
	Spent    float64 `json:"spent"`
}

type BudgetOverview struct {
	Period         string               `json:"period"`
	PeriodStart    string               `json:"period_start"`
	PeriodEnd      string               `json:"period_end"`
	TotalBudget    float64              `json:"total_budget"`
	TotalSpent     float64              `json:"total_spent"`
	TotalRemaining float64              `json:"total_remaining"`
	BudgetsOverLimit int               `json:"budgets_over_limit"`
	Budgets        []BudgetWithSpending `json:"budgets"`
}

type CategoryBrief struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type SavingGoal struct {
	ID            int64    `json:"id"`
	UserID        int64    `json:"user_id"`
	Name          string   `json:"name"`
	TargetAmount  float64  `json:"target_amount"`
	CurrentAmount float64  `json:"current_amount"`
	AccountID     *int64   `json:"account_id"`
	Deadline      *string  `json:"deadline"`
	IsCompleted   bool     `json:"is_completed"`
	Icon          *string  `json:"icon"`
	ProgressPct   int      `json:"progress_pct"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type SavingGoalDetail struct {
	SavingGoal
	DaysRemaining   *int     `json:"days_remaining,omitempty"`
	RequiredMonthly *float64 `json:"required_monthly,omitempty"`
	Account         *AccountBrief `json:"account,omitempty"`
}

type AccountBrief struct {
	ID      int64   `json:"id"`
	Name    string  `json:"name"`
	Balance float64 `json:"balance"`
}

// =============================================================
// Request Models
// =============================================================

type CreateBudgetRequest struct {
	CategoryID int64   `json:"category_id" binding:"required"`
	Amount     float64 `json:"amount" binding:"required,gt=0"`
	Period     string  `json:"period" binding:"required,oneof=weekly monthly yearly"`
}

type UpdateBudgetRequest struct {
	Amount   *float64 `json:"amount"`
	IsActive *bool    `json:"is_active"`
}

type CreateSavingGoalRequest struct {
	Name          string   `json:"name" binding:"required"`
	TargetAmount  float64  `json:"target_amount" binding:"required,gt=0"`
	AccountID     *int64   `json:"account_id"`
	Deadline      *string  `json:"deadline"`
	Icon          *string  `json:"icon"`
	CurrentAmount *float64 `json:"current_amount"`
}

type UpdateSavingGoalRequest struct {
	Name          *string  `json:"name"`
	TargetAmount  *float64 `json:"target_amount"`
	CurrentAmount *float64 `json:"current_amount"`
	AccountID     *int64   `json:"account_id"`
	Deadline      *string  `json:"deadline"`
	Icon          *string  `json:"icon"`
	IsCompleted   *bool    `json:"is_completed"`
}
