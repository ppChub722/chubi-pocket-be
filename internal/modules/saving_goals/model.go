package saving_goals

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const dateLayout = "2006-01-02"

const GroupOngoing = "ongoing"

const (
	StateActive    = "active"
	StateCompleted = "completed"
	StateArchived  = "archived"
)

const (
	KindSave     = "save"
	KindWithdraw = "withdraw"
	KindMove     = "move"

	BucketAvailable = "available"
	BucketSpare     = "spare"
	BucketGoal      = "goal"

	SourceManual    = "manual"
	SourceRecurring = "recurring"
	SourcePending   = "pending"
)

var defaultMilestones = []int{25, 50, 75, 100}

type Goal struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	Name         string
	Description  *string
	Note         *string
	IconCode     *shared.IconCode
	TargetAmount *float64
	Deadline     *time.Time
	Milestones   []int
	Status       string
	ClosedAt     *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// เป้าต่อรอบ: PeriodAmount nil = none; the periods run every PeriodInterval
	// PeriodFreq from PeriodAnchor.
	PeriodAmount   *float64
	PeriodFreq     *string
	PeriodInterval int
	PeriodAnchor   *time.Time
}

// GoalPeriod — the goal's เป้าต่อรอบ as set, and the period today is in.
type GoalPeriod struct {
	Amount   float64 `json:"amount"`
	Freq     string  `json:"freq"`
	Interval int     `json:"interval"`
	Anchor   string  `json:"anchor"`
	// Current is null when the goal isn't active, has no target or is reached.
	Current *PeriodNow `json:"current"`
}

// PeriodNow — the period today is in.
type PeriodNow struct {
	Start string `json:"start"`
	End   string `json:"end"`
	// Target is the amount, but never more than the goal still needed when the period began.
	Target   float64 `json:"target"`
	Saved    float64 `json:"saved"` // net into the goal within the period
	Short    float64 `json:"short"` // max(0, target − saved)
	DaysLeft int     `json:"days_left"`
	// EtaDate — the end of the period the goal is done in, if every period from now on
	// reaches its target (a short period isn't carried over). Late = after the deadline.
	EtaDate string `json:"eta_date"`
	Late    bool   `json:"late"`
	// Plan — what the goal's saving recurring settings put in within this period
	// (null = none).
	Plan *float64 `json:"plan"`
}

type entryRow struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	GroupID    uuid.UUID
	Kind       string
	AccountID  uuid.UUID
	FromBucket string
	FromGoalID *uuid.UUID
	ToBucket   string
	ToGoalID   *uuid.UUID
	Amount     float64
	Date       time.Time
	Note       *string
	Source     string
	CreatedAt  time.Time
}

type Milestone struct {
	Pct     int     `json:"pct"`
	Amount  float64 `json:"amount"`
	Reached bool    `json:"reached"`
}

type Actions struct {
	Save     bool `json:"save"`
	Withdraw bool `json:"withdraw"`
	Move     bool `json:"move"`
	Close    bool `json:"close"`
	Archive  bool `json:"archive"`
	Restore  bool `json:"restore"`
	Delete   bool `json:"delete"`
}

type GoalView struct {
	ID              uuid.UUID        `json:"id"`
	Name            string           `json:"name"`
	Description     *string          `json:"description"`
	Note            *string          `json:"note"`
	IconCode        *shared.IconCode `json:"icon_code"`
	TargetAmount    *float64         `json:"target_amount"` // null = no target (open-ended)
	Deadline        *string          `json:"deadline"`
	State           string           `json:"state"`
	SavedAmount     float64          `json:"saved_amount"`
	ProgressPct     *int             `json:"progress_pct"`     // null without a target
	RemainingAmount *float64         `json:"remaining_amount"` // null without a target
	DaysRemaining   *int             `json:"days_remaining"`
	NeededMonthly   *float64         `json:"needed_monthly"`
	PaceMonthly     *float64         `json:"pace_monthly"`
	EtaDate         *string          `json:"eta_date"`
	Pace            string           `json:"pace"`
	LateMonths      *int             `json:"late_months"`
	Milestones      []Milestone      `json:"milestones"`
	Currency        string           `json:"currency"`
	ClosedAt        *time.Time       `json:"closed_at"`
	Actions         Actions          `json:"actions"`
	MonthPlan       *float64         `json:"month_plan"`
	MonthSaved      float64          `json:"month_saved"`
	MonthShort      *float64         `json:"month_short"`
	Status          string           `json:"status"`
	BehindMonths    *int             `json:"behind_months"`
	Period          *GoalPeriod      `json:"period"` // null = no เป้าต่อรอบ
	Group           *string          `json:"group"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type WalletShare struct {
	Wallet   *shared.AccountRef `json:"wallet"` // nil = money with no wallet
	Amount   float64            `json:"amount"`
	SharePct int                `json:"share_pct"`
}

type Spare struct {
	Amount   float64       `json:"amount"`
	ByWallet []WalletShare `json:"by_wallet"`
}

type BucketRef struct {
	Type     string           `json:"type"`
	ID       *uuid.UUID       `json:"id"`
	Name     *string          `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
}

type Entry struct {
	ID        uuid.UUID                 `json:"id"`
	GroupID   uuid.UUID                 `json:"group_id"`
	Kind      string                    `json:"kind"`
	Date      string                    `json:"date"`
	Note      *string                   `json:"note"`
	Wallet    *shared.AccountRef        `json:"wallet"` // nil = no wallet (unallocated)
	Amount    float64                   `json:"amount"`
	From      BucketRef                 `json:"from"`
	To        BucketRef                 `json:"to"`
	Source    string                    `json:"source"`
	GoalIDs   []uuid.UUID               `json:"goal_ids"`
	Recurring *shared.RecurringItemLink `json:"recurring"`
	CreatedAt time.Time                 `json:"created_at"`
}

type EntryList struct {
	Data       []Entry     `json:"data"`
	Pagination shared.Page `json:"pagination"`
}

type HistoryPoint struct {
	Month      string  `json:"month"`
	Cumulative float64 `json:"cumulative"`
	// Plan — the cumulative plan line: min(target, k × month_plan) k months after the first point;
	// null without a plan.
	Plan *float64 `json:"plan"`
}

type ListSummary struct {
	Saved           float64 `json:"saved"`
	InGoals         float64 `json:"in_goals"`
	Spare           float64 `json:"spare"`
	SavedTotal      float64 `json:"saved_total"`
	MonthShortTotal float64 `json:"month_short_total"`
	MonthPlanTotal  float64 `json:"month_plan_total"`
	MonthSavedTotal float64 `json:"month_saved_total"`
	Unallocated     float64 `json:"unallocated"`
	Currency        string  `json:"currency"`
	// Long-term progress (the hero ring): active listed goals that have a target.
	TargetTotal      float64 `json:"target_total"`
	TargetSavedTotal float64 `json:"target_saved_total"`
	TargetShortTotal float64 `json:"target_short_total"` // Σ max(0, target − saved) per goal
	TargetPercent    int     `json:"target_percent"`     // floor(saved / target × 100), 0–100
	// Filter-chip counts; every listed goal is in exactly one (sum = len(data)).
	CountShort    int `json:"count_short"`     // has a target, not reached, not completed
	CountDone     int `json:"count_done"`      // reached or completed
	CountNoTarget int `json:"count_no_target"` // no target (ongoing)
}

type GoalList struct {
	Summary ListSummary `json:"summary"`
	Spare   Spare       `json:"spare"`
	Data    []GoalView  `json:"data"`
}

type GoalDetail struct {
	Goal          GoalView       `json:"goal"`
	ByWallet      []WalletShare  `json:"by_wallet"`
	History       []HistoryPoint `json:"history"`
	Entries       EntryList      `json:"entries"`
	DeletePreview *DeletePreview `json:"delete_preview"`
}

type SpareDetail struct {
	Spare   Spare          `json:"spare"`
	History []HistoryPoint `json:"history"`
	Entries EntryList      `json:"entries"`
}

type WalletOut struct {
	Wallet  shared.AccountRef    `json:"wallet"`
	Savings shared.WalletSavings `json:"savings"`
}

type WriteResult struct {
	Entries   []Entry      `json:"entries"`
	Goals     []GoalView   `json:"goals"`
	Spare     Spare        `json:"spare"`
	Wallets   []WalletOut  `json:"wallets"`
	GoalPage  *GoalDetail  `json:"goal_page"`
	SparePage *SpareDetail `json:"spare_page"`
}

type pageRef struct {
	goal  *uuid.UUID
	spare bool
}

type GoalRequest struct {
	Name         *string          `json:"name"`
	Description  *string          `json:"description"`
	Note         *string          `json:"note"`
	IconCode     *shared.IconCode `json:"icon_code"`
	TargetAmount *float64         `json:"target_amount"`
	Deadline     *string          `json:"deadline"`
	Milestones   []int            `json:"milestones"`
	// เป้าต่อรอบ: period_amount null clears it; period sets the periods (default:
	// every month from the 1st of this month).
	PeriodAmount *float64     `json:"period_amount"`
	Period       *PeriodInput `json:"period"`

	present map[string]bool
}

type PeriodInput struct {
	Freq     string `json:"freq"`
	Interval int    `json:"interval"`
	Anchor   string `json:"anchor"`
}

type Split struct {
	WalletID uuid.UUID `json:"wallet_id"`
	Amount   float64   `json:"amount"`
}

type EntryRequest struct {
	Kind     string     `json:"kind"`
	Amount   float64    `json:"amount"`
	Date     *string    `json:"date"`
	Note     *string    `json:"note"`
	GoalID   *uuid.UUID `json:"goal_id"`
	WalletID *uuid.UUID `json:"wallet_id"`
	ToGoalID *uuid.UUID `json:"to_goal_id"`
	Splits   []Split    `json:"splits"`
}

type EntryUpdateRequest struct {
	Amount *float64 `json:"amount"`
	Date   *string  `json:"date"`
	Note   *string  `json:"note"`

	present map[string]bool
}

type CloseRequest struct {
	To string `json:"to"`
}

type DeletePreview struct {
	Entries     int     `json:"entries"`      // saving entries of the goal that go with it
	SavedAmount float64 `json:"saved_amount"` // money it still holds
	CanDelete   bool    `json:"can_delete"`   // false while saved_amount > 0
}
