package dashboard

import (
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const (
	upcomingDays     = 14 // look-ahead window for "coming up"
	upcomingMax      = 10
	topCategoriesMax = 4 // the rest is folded into other_expense
	trendMonths      = 6
	recentMax        = 5
	pendingMax       = 5  // drafts listed in the รอยืนยัน block
	attentionMax     = 10 // notifications listed in the ต้องจัดการ block
)

const (
	KindRecurring = "recurring" // an upcoming recurring item (one-off or from a setting)
	KindCardDue   = "card_due"  // credit card / pay-later payment due date
)

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
	SavingGoals   shared.SavingTotals              `json:"saving_goals"`
	Recent        []transactions.TransactionDetail `json:"recent"`
	Pending       PendingBlock                     `json:"pending"`
	Attention     AttentionBlock                   `json:"attention"`
	Badges        shared.Badges                    `json:"badges"`
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

type CategorySlice struct {
	Category *shared.CategoryRef `json:"category"`
	Expense  float64             `json:"expense"`
	Count    int                 `json:"count"`
}

type PendingBlock struct {
	Total int                          `json:"total"`
	Items []pending.PendingTransaction `json:"items"`
}

type AttentionBlock struct {
	Total int                       `json:"total"`
	Items []shared.NotificationView `json:"items"`
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

type UpcomingItem struct {
	Kind      string           `json:"kind"`
	ID        uuid.UUID        `json:"id"` // the stored recurring item, else its setting; the account for card_due
	Name      string           `json:"name"`
	Type      string           `json:"type"` // income | expense
	Amount    float64          `json:"amount"`
	DueDate   string           `json:"due_date"`
	DaysUntil int              `json:"days_until"`
	Overdue   bool             `json:"overdue"`
	IconCode  *shared.IconCode `json:"icon_code"`
	LogoURL   *string          `json:"logo_url"`
	// RuleID / Slot say which occurrence a recurring row is (its setting and its
	// original date): the id alone repeats for a daily setting. Null for card_due.
	RuleID *uuid.UUID `json:"rule_id"`
	Slot   *string    `json:"slot"`
}

type BudgetsTile struct {
	Count          int     `json:"count"`
	TotalBudget    float64 `json:"total_budget"`
	TotalSpent     float64 `json:"total_spent"`
	UtilizationPct float64 `json:"utilization_pct"`
	OverLimitCount int     `json:"over_limit_count"`
}

type DebtsTile = personal_debts.DebtTotals
