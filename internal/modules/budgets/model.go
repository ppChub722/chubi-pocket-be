package budgets

// Budgets v2 (0.3.1-10, owner mockup 2026-10-13). A budget is a NAMED
// personal tracker:
//   - SCOPE (free; budgets may overlap): everything, or any mix of
//     categories (± sub-categories), tag NAMES and wallets — a row counts
//     when it matches ANY of them;
//   - TIME: recurring ("งบระยะยาว", periods from a rule; each period is an
//     instance with its own amount / note) or one range (fixed, or open
//     until ปิดงบ);
//   - WHAT COUNTS (expense side, my share): USED = my transactions (normal /
//     recurring-linked) + my share of event bills (once); EXPECTED (current
//     period only) = upcoming recurring items + pending drafts.

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const dateLayout = "2006-01-02"

// Status values (stored).
const (
	StatusActive   = "active"
	StatusArchived = "archived"
)

// States (derived).
const (
	StateUpcoming = "upcoming"
	StateActive   = "active"
	StateEnded    = "ended"
	StateArchived = "archived"
)

// Period statuses.
const (
	PeriodOK   = "ok"
	PeriodPace = "pace"
	PeriodWarn = "warn"
	PeriodOver = "over"
)

// Sources of what a budget counts.
const (
	SourceNormal    = "normal"
	SourceRecurring = "recurring"
	SourceEvent     = "event"
	SourceUpcoming  = "upcoming"
	SourcePending   = "pending"
)

const defaultWarnPct = 80

// scopeCat — one category of the scope.
type scopeCat struct {
	ID              uuid.UUID
	IncludeChildren bool
}

// Budget — the budgets row + its scope lists.
type Budget struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	Name        string
	Description *string
	Note        *string
	IconCode    *shared.IconCode
	Amount      float64
	WarnPct     *int
	ScopeAll    bool
	TagNames    []string
	Recurring   bool
	Freq        *string
	Interval    int
	StartDate   time.Time
	AnchorDate  time.Time
	EndKind     string
	EndDate     *time.Time
	EndCount    *int
	ClosedOn    *time.Time
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Categories  []scopeCat
	Wallets     []uuid.UUID
}

// periodRow — a budget_periods row.
type periodRow struct {
	ID         uuid.UUID
	Seq        int
	Start      time.Time
	End        *time.Time
	Amount     float64
	Overridden bool
	Note       *string
}

// --- API shapes (spec schemas Budget*) ---

type ScopeCategory struct {
	Category        shared.CategoryRef `json:"category"`
	IncludeChildren bool               `json:"include_children"`
}

// Scope — spec BudgetScope.
type Scope struct {
	All        bool                `json:"all"`
	Categories []ScopeCategory     `json:"categories"`
	Tags       []string            `json:"tags"`
	Wallets    []shared.AccountRef `json:"wallets"`
}

// Rule — spec BudgetRule.
type Rule struct {
	Freq     string `json:"freq"`
	Interval int    `json:"interval"`
}

// End — spec RecurrenceEnd.
type End struct {
	Kind  string  `json:"kind"`
	Date  *string `json:"date"`
	Count *int    `json:"count"`
}

// Period — spec BudgetPeriod.
type Period struct {
	ID         uuid.UUID `json:"id"`
	Seq        int       `json:"seq"`
	Start      string    `json:"start"`
	End        *string   `json:"end"`
	Amount     float64   `json:"amount"`
	BaseAmount float64   `json:"base_amount"`
	Overridden bool      `json:"overridden"`
	Note       *string   `json:"note"`
	IsCurrent  bool      `json:"is_current"`
	Used       float64   `json:"used"`
	Expected   float64   `json:"expected"`
	Remaining  float64   `json:"remaining"`
	OverBy     float64   `json:"over_by"`
	Pct        int       `json:"pct"`
	Status     string    `json:"status"`
	Tone       string    `json:"tone"`
	DaysLeft   *int      `json:"days_left"`
	// OnPace — the forecast says this period will end over its amount (the
	// "on pace to exceed" row badge); independent of status (a warn period can
	// be on pace too).
	OnPace bool `json:"on_pace_to_exceed"`
}

type Actions struct {
	Edit    bool `json:"edit"`
	Close   bool `json:"close"`
	Archive bool `json:"archive"`
	Restore bool `json:"restore"`
}

// View — spec Budget.
type View struct {
	ID          uuid.UUID        `json:"id"`
	Name        string           `json:"name"`
	Description *string          `json:"description"`
	Note        *string          `json:"note"`
	IconCode    *shared.IconCode `json:"icon_code"`
	Amount      float64          `json:"amount"`
	WarnPct     *int             `json:"warn_pct"`
	Scope       Scope            `json:"scope"`
	Recurring   bool             `json:"recurring"`
	Rule        *Rule            `json:"rule"`
	StartDate   string           `json:"start_date"`
	End         End              `json:"end"`
	State       string           `json:"state"`
	ClosedOn    *string          `json:"closed_on"`
	Period      Period           `json:"period"`
	Actions     Actions          `json:"actions"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// ListSummary — spec BudgetListSummary.
type ListSummary struct {
	ActiveCount   int `json:"active_count"`
	OverCount     int `json:"over_count"`
	WarnCount     int `json:"warn_count"`
	PaceCount     int `json:"pace_count"`
	EndedCount    int `json:"ended_count"`
	ArchivedCount int `json:"archived_count"`

	// The summary card (spec BudgetListSummary): over the active budgets, each at
	// its current period; used counts a transaction once across overlapping budgets.
	TotalLimit     float64      `json:"total_limit"`
	TotalUsed      float64      `json:"total_used"`
	TotalRemaining float64      `json:"total_remaining"`
	UsedPct        int          `json:"used_pct"`
	Counts         HealthCounts `json:"counts"`
}

// List — spec BudgetList.
type List struct {
	Summary ListSummary `json:"summary"`
	Data    []View      `json:"data"`
}

// Forecast — spec BudgetForecast.
type Forecast struct {
	ElapsedDays            *int     `json:"elapsed_days"`
	TotalDays              *int     `json:"total_days"`
	DailyAllowance         *float64 `json:"daily_allowance"`
	AvgDaily               *float64 `json:"avg_daily"`
	AvgMonthly             *float64 `json:"avg_monthly"`
	ForecastEnd            *float64 `json:"forecast_end"`
	ForecastOverBy         *float64 `json:"forecast_over_by"`
	ForecastOverDate       *string  `json:"forecast_over_date"`
	RemainingAfterExpected float64  `json:"remaining_after_expected"`
	ThisMonth              *float64 `json:"this_month"`
	HistoryAvg             *float64 `json:"history_avg"`
	HistoryCount           int      `json:"history_count"`
	// 0.4.0 detail numbers: available = limit − used; projected_end = forecast_end;
	// days_left (today included) and pace_pct = elapsed / total days (the pace
	// tick); the last three are null for an open-ended or non-current period.
	Available    float64  `json:"available"`
	ProjectedEnd *float64 `json:"projected_end"`
	DaysLeft     *int     `json:"days_left"`
	PacePct      *int     `json:"pace_pct"`
}

// SourceRow — spec BudgetSourceRow.
type SourceRow struct {
	Source   string  `json:"source"`
	CountsAs string  `json:"counts_as"`
	Count    int     `json:"count"`
	Amount   float64 `json:"amount"`
}

// CategoryRow — spec BudgetCategoryRow.
type CategoryRow struct {
	Category *shared.CategoryRef `json:"category"`
	Amount   float64             `json:"amount"`
	Pct      int                 `json:"pct"`
}

// Item — spec BudgetItem.
type Item struct {
	Kind        string                    `json:"kind"`
	ID          uuid.UUID                 `json:"id"`
	Source      string                    `json:"source"`
	CountsAs    string                    `json:"counts_as"`
	Date        *string                   `json:"date"`
	Amount      float64                   `json:"amount"`
	Description *string                   `json:"description"`
	Category    *shared.CategoryRef       `json:"category"`
	Account     *shared.AccountRef        `json:"account"`
	Tags        []string                  `json:"tags"`
	Event       *shared.EventRef          `json:"event"`
	Recurring   *shared.RecurringItemLink `json:"recurring"`
	// Reason — why the row counts here: all | category | sub_category | tag | wallet.
	Reason string `json:"reason"`
}

type ItemList struct {
	Data       []Item      `json:"data"`
	Pagination shared.Page `json:"pagination"`
}

type PeriodList struct {
	Data       []Period    `json:"data"`
	Pagination shared.Page `json:"pagination"`
}

type RecentPeriods struct {
	Data    []Period `json:"data"`
	HasMore bool     `json:"has_more"`
}

// Detail — spec BudgetDetail.
type Detail struct {
	Budget     View          `json:"budget"`
	Forecast   Forecast      `json:"forecast"`
	BySource   []SourceRow   `json:"by_source"`
	ByCategory []CategoryRow `json:"by_category"`
	Periods    RecentPeriods `json:"periods"`
	Items      ItemList      `json:"items"`
}

// Totals — the Home budgets tile / More budgets_summary: every active,
// started budget at its current period.
type Totals struct {
	Count          int
	TotalBudget    float64
	TotalSpent     float64
	UtilizationPct float64
	OverLimitCount int
}

// --- requests ---

type ScopeCategoryInput struct {
	ID              uuid.UUID `json:"id"`
	IncludeChildren *bool     `json:"include_children"`
}

// ScopeInput — spec BudgetScopeInput.
type ScopeInput struct {
	All        bool                 `json:"all"`
	Categories []ScopeCategoryInput `json:"categories"`
	Tags       []string             `json:"tags"`
	WalletIDs  []uuid.UUID          `json:"wallet_ids"`
}

type RuleInput struct {
	Freq     string `json:"freq"`
	Interval int    `json:"interval"`
}

// Request — create / update body (spec BudgetCreateRequest /
// BudgetUpdateRequest); presence filled by the handler.
type Request struct {
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	Note        *string          `json:"note"`
	IconCode    *shared.IconCode `json:"icon_code"`
	Amount      *float64         `json:"amount"`
	WarnPct     *int             `json:"warn_pct"`
	Scope       *ScopeInput      `json:"scope"`
	Recurring   *bool            `json:"recurring"`
	Rule        *RuleInput       `json:"rule"`
	StartDate   *string          `json:"start_date"`
	End         *End             `json:"end"`
	Apply       string           `json:"apply"`

	present map[string]bool
}

func (r *Request) has(k string) bool { return r.present[k] }

// PeriodRequest — spec BudgetPeriodUpdateRequest.
type PeriodRequest struct {
	Amount *float64 `json:"amount"`
	Note   *string  `json:"note"`

	present map[string]bool
}

// HealthCounts — ok + near + over = all (the active budgets).
type HealthCounts struct {
	All  int `json:"all"`
	OK   int `json:"ok"`
	Near int `json:"near"`
	Over int `json:"over"`
}
