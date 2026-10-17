package saving_goals

// Saving goals v2 (0.3.1-10, owner mockup goals-v2.html). Savings are a
// NOTEBOOK: a saving entry moves an amount of ONE wallet between buckets —
// the wallet's available money, the spare bucket ("ยังไม่จัดสรร") or a
// goal — and never touches transactions or wallet balances. Kinds: save
// (available → spare / goal), withdraw (spare / goal → its wallet), move
// (spare / goal → spare / goal; the wallet travels with it). A (bucket,
// wallet) never goes below 0; saved above a wallet's balance is a red
// warning only.

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const dateLayout = "2006-01-02"

// Goal states.
const (
	StateActive    = "active"
	StateCompleted = "completed"
	StateArchived  = "archived"
)

// Entry kinds / buckets / sources.
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

// Goal — the saving_goals row.
type Goal struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	Name         string
	Description  *string
	Note         *string
	IconCode     *shared.IconCode
	TargetAmount float64
	Deadline     *time.Time
	Milestones   []int
	Status       string
	ClosedAt     *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// entryRow — the saving_entries row.
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

// --- API shapes (spec schemas Saving*) ---

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

// GoalView — spec SavingGoal.
type GoalView struct {
	ID              uuid.UUID        `json:"id"`
	Name            string           `json:"name"`
	Description     *string          `json:"description"`
	Note            *string          `json:"note"`
	IconCode        *shared.IconCode `json:"icon_code"`
	TargetAmount    float64          `json:"target_amount"`
	Deadline        *string          `json:"deadline"`
	State           string           `json:"state"`
	SavedAmount     float64          `json:"saved_amount"`
	ProgressPct     int              `json:"progress_pct"`
	RemainingAmount float64          `json:"remaining_amount"`
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
	// 0.4.0 page numbers (spec 09 §0.7): this month the goal is planned to get
	// month_plan (its auto-saving amount, else needed_monthly), month_saved is what
	// went in so far (net), month_short = month_plan − month_saved (≥ 0).
	// status: reached | on_track | behind | none (behind_months = how late).
	MonthPlan    *float64  `json:"month_plan"`
	MonthSaved   float64   `json:"month_saved"`
	MonthShort   *float64  `json:"month_short"`
	Status       string    `json:"status"`
	BehindMonths *int      `json:"behind_months"`
	AutoSave     *AutoSave `json:"auto_save"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// WalletShare — spec SavingWalletShare.
type WalletShare struct {
	Wallet   *shared.AccountRef `json:"wallet"` // nil = money with no wallet
	Amount   float64            `json:"amount"`
	SharePct int                `json:"share_pct"`
}

// Spare — spec SavingSpare.
type Spare struct {
	Amount   float64       `json:"amount"`
	ByWallet []WalletShare `json:"by_wallet"`
}

// BucketRef — spec SavingBucketRef.
type BucketRef struct {
	Type     string           `json:"type"`
	ID       *uuid.UUID       `json:"id"`
	Name     *string          `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
}

// Entry — spec SavingEntry.
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
	// Plan — the cumulative plan line: min(target, k × month_plan) k months after the first point; null without a plan.
	Plan *float64 `json:"plan"`
}

type ListSummary struct {
	Saved   float64 `json:"saved"`
	InGoals float64 `json:"in_goals"`
	Spare   float64 `json:"spare"`
	// 0.4.0: saved_total = every saving (goals + unallocated, wallet-less too),
	// month_short_total = Σ the goals' month_short, unallocated = the spare bucket.
	SavedTotal      float64 `json:"saved_total"`
	MonthShortTotal float64 `json:"month_short_total"`
	// month_plan_total = Σ the active goals' month_plan, month_saved_total = Σ their month_saved
	// (the card bar "ออมแล้ว X / Y").
	MonthPlanTotal  float64 `json:"month_plan_total"`
	MonthSavedTotal float64 `json:"month_saved_total"`
	Unallocated     float64 `json:"unallocated"`
	Currency        string  `json:"currency"`
}

// GoalList — spec SavingGoalList.
type GoalList struct {
	Summary ListSummary `json:"summary"`
	Spare   Spare       `json:"spare"`
	Data    []GoalView  `json:"data"`
}

// GoalDetail — spec SavingGoalDetail.
type GoalDetail struct {
	Goal     GoalView       `json:"goal"`
	ByWallet []WalletShare  `json:"by_wallet"`
	History  []HistoryPoint `json:"history"`
	Entries  EntryList      `json:"entries"`
	// DeletePreview — only with ?include=delete_preview, else null.
	DeletePreview *DeletePreview `json:"delete_preview"`
}

// SpareDetail — spec SavingSpareDetail.
type SpareDetail struct {
	Spare    Spare          `json:"spare"`
	History  []HistoryPoint `json:"history"`
	AutoSave *AutoSave      `json:"auto_save"`
	Entries  EntryList      `json:"entries"`
}

type WalletOut struct {
	Wallet  shared.AccountRef    `json:"wallet"`
	Savings shared.WalletSavings `json:"savings"`
}

// WriteResult — spec SavingWriteResult.
type WriteResult struct {
	Entries []Entry     `json:"entries"`
	Goals   []GoalView  `json:"goals"`
	Spare   Spare       `json:"spare"`
	Wallets []WalletOut `json:"wallets"`
	// The page the action was made from (one button = one call).
	GoalPage  *GoalDetail  `json:"goal_page"`
	SparePage *SpareDetail `json:"spare_page"`
}

// pageRef — which page a write answers with: a goal's, or spare's.
type pageRef struct {
	goal  *uuid.UUID
	spare bool
}

// --- requests ---

// GoalRequest — create / update (presence filled by the handler).
type GoalRequest struct {
	Name         *string          `json:"name"`
	Description  *string          `json:"description"`
	Note         *string          `json:"note"`
	IconCode     *shared.IconCode `json:"icon_code"`
	TargetAmount *float64         `json:"target_amount"`
	Deadline     *string          `json:"deadline"`
	Milestones   []int            `json:"milestones"`

	present map[string]bool
}

type Split struct {
	WalletID uuid.UUID `json:"wallet_id"`
	Amount   float64   `json:"amount"`
}

// EntryRequest — spec SavingEntryRequest.
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

// EntryUpdateRequest — spec SavingEntryUpdateRequest.
type EntryUpdateRequest struct {
	Amount *float64 `json:"amount"`
	Date   *string  `json:"date"`
	Note   *string  `json:"note"`

	present map[string]bool
}

type CloseRequest struct {
	To string `json:"to"`
}

// DeletePreview — what deleting the goal does: it is only allowed while the
// goal holds no money (else 409 GOAL_HAS_SAVINGS: close it first).
type DeletePreview struct {
	Entries     int     `json:"entries"`      // saving entries of the goal that go with it
	SavedAmount float64 `json:"saved_amount"` // money it still holds
	CanDelete   bool    `json:"can_delete"`   // false while saved_amount > 0
}
