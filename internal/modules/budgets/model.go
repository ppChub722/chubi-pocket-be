package budgets

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Scope values.
const (
	ScopeUser    = "user"
	ScopeProject = "project"
)

// Period values.
const (
	PeriodWeekly  = "weekly"
	PeriodMonthly = "monthly"
	PeriodYearly  = "yearly"
)

// Status values.
const (
	StatusActive   = "active"
	StatusArchived = "archived"
)

// Budget — stored row, no computed progress fields.
//
// Migration 000037 dropped `icon_code` (icon comes from the linked
// category) and added `description` as the primary label. Migration
// 000038 added `note` for the longer free-form narrative shown on the
// detail page — same description/note split accounts use.
type Budget struct {
	ID          uuid.UUID  `json:"id"`
	UserID      uuid.UUID  `json:"user_id"`
	CategoryID  uuid.UUID  `json:"category_id"`
	Scope       string     `json:"scope"`
	ProjectID   *uuid.UUID `json:"project_id"`
	Amount      float64    `json:"amount"`
	Period      string     `json:"period"`
	Currency    string     `json:"currency"`
	Status      string     `json:"status"`
	Description *string    `json:"description"`
	Note        *string    `json:"note"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// CategorySummary — embedded snapshot used in read responses.
type CategorySummary struct {
	ID       uuid.UUID        `json:"id"`
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code,omitempty"`
}

// CurrentPeriod — period-boundary snapshot + spend computed at read.
type CurrentPeriod struct {
	Start          string  `json:"start"` // YYYY-MM-DD
	End            string  `json:"end"`
	Spent          float64 `json:"spent"`
	Remaining      float64 `json:"remaining"`
	UtilizationPct float64 `json:"utilization_pct"`
	OverLimit      bool    `json:"over_limit"`
}

// ChildBreakdownRow — descendant-by-descendant spend roll-up.
type ChildBreakdownRow struct {
	CategoryID uuid.UUID `json:"category_id"`
	Name       string    `json:"name"`
	Spent      float64   `json:"spent"`
}

// BudgetView — read-shape with computed fields.
type BudgetView struct {
	Budget
	Category       CategorySummary     `json:"category"`
	CurrentPeriod  CurrentPeriod       `json:"current_period"`
	ChildBreakdown []ChildBreakdownRow `json:"child_breakdown"`
}

// CreateRequest — `currency` is optional; defaults to user's currency
// (THB in 1c). `project_id` required iff `scope='project'`.
// `description` is the optional primary label; `note` is optional
// long-form narrative.
type CreateRequest struct {
	CategoryID  uuid.UUID  `json:"category_id" binding:"required"`
	Amount      float64    `json:"amount"      binding:"required,gt=0"`
	Period      string     `json:"period"      binding:"required,oneof=weekly monthly yearly"`
	Scope       string     `json:"scope"       binding:"required,oneof=user project"`
	ProjectID   *uuid.UUID `json:"project_id"  binding:"omitempty"`
	Currency    *string    `json:"currency"    binding:"omitempty,len=3"`
	Description *string    `json:"description" binding:"omitempty,max=200"`
	Note        *string    `json:"note"`
}

// UpdateRequest — partial. category_id / scope / project_id NOT
// editable (delete + recreate). status changes flow via archive /
// restore endpoints.
type UpdateRequest struct {
	Amount      *float64 `json:"amount"      binding:"omitempty,gt=0"`
	Period      *string  `json:"period"      binding:"omitempty,oneof=weekly monthly yearly"`
	Currency    *string  `json:"currency"    binding:"omitempty,len=3"`
	Description *string  `json:"description" binding:"omitempty,max=200"`
	Note        *string  `json:"note"`
}

type ListFilter struct {
	Status    string // 'active' (default), 'archived', 'all'
	Period    string // optional period filter
	Scope     string // 'user', 'project', 'all'
	ProjectID *uuid.UUID
}

type ListResponse struct {
	Data []BudgetView `json:"data"`
}

// OverviewItem — compact per-budget summary used in /overview.
type OverviewItem struct {
	ID             uuid.UUID `json:"id"`
	CategoryName   string    `json:"category_name"`
	Amount         float64   `json:"amount"`
	Spent          float64   `json:"spent"`
	OverLimit      bool      `json:"over_limit"`
	UtilizationPct float64   `json:"utilization_pct"`
}

type OverviewResponse struct {
	Period                string         `json:"period"`
	PeriodStart           string         `json:"period_start"`
	PeriodEnd             string         `json:"period_end"`
	Scope                 string         `json:"scope"`
	ProjectID             *uuid.UUID     `json:"project_id"`
	TotalBudget           float64        `json:"total_budget"`
	TotalSpent            float64        `json:"total_spent"`
	TotalRemaining        float64        `json:"total_remaining"`
	OverallUtilizationPct float64        `json:"overall_utilization_pct"`
	BudgetsOverLimit      int            `json:"budgets_over_limit"`
	Budgets               []OverviewItem `json:"budgets"`
}
