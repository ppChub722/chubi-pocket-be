package budgets

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const dateLayout = "2006-01-02"

const (
	StatusActive   = "active"
	StatusArchived = "archived"
)

const (
	StateUpcoming = "upcoming"
	StateActive   = "active"
	StateEnded    = "ended"
	StateArchived = "archived"
)

const (
	PeriodOK   = "ok"
	PeriodPace = "pace"
	PeriodWarn = "warn"
	PeriodOver = "over"
)

const (
	SourceNormal    = "normal"
	SourceRecurring = "recurring"
	SourceEvent     = "event"
	SourceUpcoming  = "upcoming"
	SourcePending   = "pending"
	// SourceFutureTx — a future-dated transaction: รอจ่าย until its day.
	SourceFutureTx = "future_tx"
)

const defaultWarnPct = 80

type scopeCat struct {
	ID              uuid.UUID
	IncludeChildren bool
}

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

type periodRow struct {
	ID         uuid.UUID
	Seq        int
	Start      time.Time
	End        *time.Time
	Amount     float64
	Overridden bool
	Note       *string
	// Base is the amount without its own: the budget's amount for this period
	// (kept when the budget's amount changes later, so a past period stays).
	Base float64
}

type ScopeCategory struct {
	Category        shared.CategoryRef `json:"category"`
	IncludeChildren bool               `json:"include_children"`
}

type Scope struct {
	All        bool                `json:"all"`
	Categories []ScopeCategory     `json:"categories"`
	Tags       []string            `json:"tags"`
	Wallets    []shared.AccountRef `json:"wallets"`
}

type Rule struct {
	Freq     string `json:"freq"`
	Interval int    `json:"interval"`
}

type End struct {
	Kind  string  `json:"kind"`
	Date  *string `json:"date"`
	Count *int    `json:"count"`
}

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
	PacePct *int `json:"pace_pct"`
	OnPace bool `json:"on_pace_to_exceed"`
}

type Actions struct {
	Edit    bool `json:"edit"`
	Close   bool `json:"close"`
	Archive bool `json:"archive"`
	Restore bool `json:"restore"`
}

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

type ListSummary struct {
	ActiveCount   int `json:"active_count"`
	OverCount     int `json:"over_count"`
	WarnCount     int `json:"warn_count"`
	PaceCount     int `json:"pace_count"`
	EndedCount    int `json:"ended_count"`
	ArchivedCount int `json:"archived_count"`

	TotalLimit     float64      `json:"total_limit"`
	TotalUsed      float64      `json:"total_used"`
	TotalRemaining float64      `json:"total_remaining"`
	UsedPct        int          `json:"used_pct"`
	Counts         HealthCounts `json:"counts"`
}

type List struct {
	Summary ListSummary `json:"summary"`
	Data    []View      `json:"data"`
}

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
	Available    float64  `json:"available"`
	ProjectedEnd *float64 `json:"projected_end"`
	DaysLeft     *int     `json:"days_left"`
	PacePct      *int     `json:"pace_pct"`
}

type SourceRow struct {
	Source   string  `json:"source"`
	CountsAs string  `json:"counts_as"`
	Count    int     `json:"count"`
	Amount   float64 `json:"amount"`
}

type CategoryRow struct {
	Category *shared.CategoryRef `json:"category"`
	Amount   float64             `json:"amount"`
	Pct      int                 `json:"pct"`
}

// ItemTag is a tag name on a budget item; icon_code is null for a name that is
// not one of the user's tags (an event or recurring tag).
type ItemTag struct {
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
}

// ItemRecurring links a budget item to its recurring setting. item_id is null
// while the occurrence is only computed (no stored row yet).
type ItemRecurring struct {
	ItemID    *uuid.UUID           `json:"item_id"`
	Recurring *shared.RecurringRef `json:"recurring"`
	Seq       *int                 `json:"seq"`
	Total     *int                 `json:"total"`
}

type Item struct {
	Kind        string              `json:"kind"`
	ID          uuid.UUID           `json:"id"`
	Source      string              `json:"source"`
	CountsAs    string              `json:"counts_as"`
	Date        *string             `json:"date"`
	Amount      float64             `json:"amount"`
	Description *string             `json:"description"`
	Category    *shared.CategoryRef `json:"category"`
	Account     *shared.AccountRef  `json:"account"`
	Tags        []string            `json:"tags"`
	TagRefs     []ItemTag           `json:"tag_refs"` // the same tags with their icon
	Event       *shared.EventRef    `json:"event"`
	Recurring   *ItemRecurring      `json:"recurring"`
	// RuleID / Slot name a recurring occurrence (the id of its setting and its
	// original date); set for the รอจ่าย rows read from computed occurrences.
	RuleID *uuid.UUID `json:"rule_id"`
	Slot   *string    `json:"slot"`
	Reason string     `json:"reason"`
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

type Detail struct {
	Budget     View          `json:"budget"`
	Forecast   Forecast      `json:"forecast"`
	BySource   []SourceRow   `json:"by_source"`
	ByCategory []CategoryRow `json:"by_category"`
	Periods    RecentPeriods `json:"periods"`
	Items      ItemList      `json:"items"`
	DeletePreview *DeletePreview `json:"delete_preview"`
}

type Totals struct {
	Count          int
	TotalBudget    float64
	TotalSpent     float64
	UtilizationPct float64
	OverLimitCount int
}

type ScopeCategoryInput struct {
	ID              uuid.UUID `json:"id"`
	IncludeChildren *bool     `json:"include_children"`
}

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

type PeriodRequest struct {
	Amount *float64 `json:"amount"`
	Note   *string  `json:"note"`

	present map[string]bool
}

type HealthCounts struct {
	All  int `json:"all"`
	OK   int `json:"ok"`
	Near int `json:"near"`
	Over int `json:"over"`
}

type DeletePreview struct {
	Periods           int `json:"periods"`
	OverriddenPeriods int `json:"overridden_periods"` // periods with their own amount
	Categories        int `json:"categories"`
	Wallets           int `json:"wallets"`
}
