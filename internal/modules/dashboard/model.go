package dashboard

import (
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Limits — keep the payload a single glanceable screen (contract §4).
const (
	upcomingDays     = 14 // look-ahead window for "coming up"
	upcomingMax      = 10
	topCategoriesMax = 4 // the rest is folded into other_expense
	trendMonths      = 6
	recentMax        = 5
)

// Upcoming item kinds.
const (
	KindScheduled = "scheduled" // subscription / recurring / installment
	KindCardDue   = "card_due"  // credit card / pay-later payment due date
)

// Response is GET /v1/dashboard. Month-scoped blocks (summary, previous,
// categories, trend) follow ?month=; the rest is a "right now" snapshot.
// Empty sections are zero values / [] — never omitted.
type Response struct {
	Month    string `json:"month"` // YYYY-MM
	From     string `json:"from"`
	To       string `json:"to"`
	Today    string `json:"today"` // in the caller's timezone
	Currency string `json:"currency"`

	NetWorth      NetWorth                         `json:"net_worth"`
	Summary       Totals                           `json:"summary"`
	Previous      Totals                           `json:"previous"` // the month before ?month=
	TopCategories []CategorySlice                  `json:"top_categories"`
	OtherExpense  float64                          `json:"other_expense"` // expense outside top_categories
	Trend         []TrendPoint                     `json:"trend"`         // oldest → newest, ends at ?month=
	Upcoming      Upcoming                         `json:"upcoming"`
	Budgets       BudgetsTile                      `json:"budgets"`
	Debts         DebtsTile                        `json:"debts"`
	SavingGoals   GoalsTile                        `json:"saving_goals"`
	Recent        []transactions.TransactionDetail `json:"recent"`
}

type NetWorth struct {
	Total         float64 `json:"total"`
	Assets        float64 `json:"assets"`
	Liabilities   float64 `json:"liabilities"` // positive number
	AccountsCount int     `json:"accounts_count"`
}

type Totals struct {
	Income           float64 `json:"income"`
	Expense          float64 `json:"expense"`
	Net              float64 `json:"net"`
	TransactionCount int     `json:"transaction_count"`
}

// CategorySlice is one top-level (parent) expense category.
// CategoryID nil = uncategorized.
type CategorySlice struct {
	CategoryID *uuid.UUID       `json:"category_id"`
	Name       string           `json:"name"`
	IconCode   *shared.IconCode `json:"icon_code"`
	Expense    float64          `json:"expense"`
	Count      int              `json:"count"`
}

type TrendPoint struct {
	Month   string  `json:"month"` // YYYY-MM
	Income  float64 `json:"income"`
	Expense float64 `json:"expense"`
}

type Upcoming struct {
	Days         int            `json:"days"`
	Items        []UpcomingItem `json:"items"`
	TotalExpense float64        `json:"total_expense"`
	TotalIncome  float64        `json:"total_income"`
}

// UpcomingItem — DaysUntil is negative when overdue (scheduled only: a
// card due date always rolls forward to the next occurrence).
type UpcomingItem struct {
	Kind      string           `json:"kind"`
	ID        uuid.UUID        `json:"id"` // scheduled tx id, or account id for card_due
	Name      string           `json:"name"`
	Type      string           `json:"type"` // income | expense
	Amount    float64          `json:"amount"`
	DueDate   string           `json:"due_date"`
	DaysUntil int              `json:"days_until"`
	Overdue   bool             `json:"overdue"`
	IconCode  *shared.IconCode `json:"icon_code"`
	LogoURL   *string          `json:"logo_url"`
}

type BudgetsTile struct {
	Count          int     `json:"count"`
	TotalBudget    float64 `json:"total_budget"`
	TotalSpent     float64 `json:"total_spent"`
	UtilizationPct float64 `json:"utilization_pct"`
	OverLimitCount int     `json:"over_limit_count"`
}

type DebtsTile struct {
	OwedToMe  float64 `json:"owed_to_me"`
	IOwe      float64 `json:"i_owe"`
	Net       float64 `json:"net"`
	OpenCount int     `json:"open_count"`
}

type GoalsTile struct {
	Count          int     `json:"count"`
	CompletedCount int     `json:"completed_count"`
	TotalTarget    float64 `json:"total_target"`
	TotalCurrent   float64 `json:"total_current"`
	ProgressPct    float64 `json:"progress_pct"`
}
