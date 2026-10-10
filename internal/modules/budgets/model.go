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
// The icon comes from the linked category (migration 000037). Like every
// "thing": `name` (the title — defaults to the category's name, migration
// 000051), `description`, `note`.
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
	Name        string     `json:"name"`
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
// `name` is optional: absent / null / "" → the category's name.
type CreateRequest struct {
	CategoryID  uuid.UUID  `json:"category_id" binding:"required"`
	Amount      float64    `json:"amount"      binding:"required,gt=0"`
	Period      string     `json:"period"      binding:"required,oneof=weekly monthly yearly"`
	Scope       string     `json:"scope"       binding:"required,oneof=user project"`
	ProjectID   *uuid.UUID `json:"project_id"  binding:"omitempty"`
	Currency    *string    `json:"currency"    binding:"omitempty,len=3"`
	Name        *string    `json:"name"        binding:"omitempty,max=100"`
	Description *string    `json:"description" binding:"omitempty,max=200"`
	Note        *string    `json:"note"        binding:"omitempty,max=500"`
}

// UpdateRequest — partial. scope / project_id NOT editable (delete +
// recreate). status changes flow via archive / restore endpoints.
//
// CategoryID re-anchors the budget on another expense category. Spent is
// computed live from transactions, so nothing needs migrating; the
// one-active-budget-per-(category, period, scope) rule still applies.
type UpdateRequest struct {
	CategoryID  *uuid.UUID `json:"category_id"`
	Amount      *float64   `json:"amount"      binding:"omitempty,gt=0"`
	Period      *string    `json:"period"      binding:"omitempty,oneof=weekly monthly yearly"`
	Currency    *string    `json:"currency"    binding:"omitempty,len=3"`
	Name        *string    `json:"name"        binding:"omitempty,max=100"`
	Description *string    `json:"description" binding:"omitempty,max=200"`
	Note        *string    `json:"note"        binding:"omitempty,max=500"`

	namePresent        bool
	descriptionPresent bool
	notePresent        bool
}

// UnmarshalJSON records which keys the body carried: an absent name /
// description / note stays as is; null or "" clears it (name: back to the
// category's name).
func (r *UpdateRequest) UnmarshalJSON(data []byte) error {
	type alias UpdateRequest
	present, err := shared.DecodeTracked(data, (*alias)(r))
	r.namePresent = present["name"]
	r.descriptionPresent, r.notePresent = present["description"], present["note"]
	return err
}

func (r *UpdateRequest) NameChange() (*string, bool) {
	return shared.TextChange(r.Name, r.namePresent)
}

func (r *UpdateRequest) DescriptionChange() (*string, bool) {
	return shared.TextChange(r.Description, r.descriptionPresent)
}

func (r *UpdateRequest) NoteChange() (*string, bool) {
	return shared.TextChange(r.Note, r.notePresent)
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
