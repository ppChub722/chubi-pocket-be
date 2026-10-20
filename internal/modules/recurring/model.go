package recurring

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
	"github.com/ppChub722/chubi-pocket-be/internal/shared/recurrence"
)

const dateLayout = recurrence.DateLayout

// Item statuses: upcoming (date after today) · due (date today or before, not
// recorded) · done (a transaction) · paid (cleared without a transaction) ·
// skipped · pending (a legacy draft still waiting). StatusNotCreated and
// StatusArchived are the old names (= due, = skipped), still accepted as filters.
const (
	StatusUpcoming   = "upcoming"
	StatusDue        = "due"
	StatusPending    = "pending"
	StatusDone       = "done"
	StatusPaid       = "paid"
	StatusSkipped    = "skipped"
	StatusNotCreated = "not_created"
	StatusArchived   = "archived"
)

// statusSQL is the status of a STORED item row (alias i); a computed
// occurrence has no row and gets its status in Go.
var statusSQL = `(CASE WHEN i.transaction_id IS NOT NULL OR i.saving_entry_id IS NOT NULL THEN 'done'
	WHEN i.archived THEN 'skipped'
	WHEN i.cleared_at IS NOT NULL THEN 'paid'
	WHEN i.pending_id IS NOT NULL THEN 'pending'
	WHEN i.date <= ` + shared.TodaySQL("i.user_id") + ` THEN 'due'
	ELSE 'upcoming' END)`

type Rule struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	Name           string
	Description    *string
	Note           *string
	IconCode       *shared.IconCode
	Type           string
	Amount         float64
	AccountID      *uuid.UUID
	ToAccountID    *uuid.UUID
	CategoryID     *uuid.UUID
	GoalID         *uuid.UUID // type saving: the goal (nil = spare)
	TagIDs         []uuid.UUID
	recurrence.Spec
	Kind       string     // subscription | installment | income | reminder
	TrialUntil *time.Time // subscription: the free trial runs until
	Status     string
	EndingNotified bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// TrackedFrom — occurrences before it are history (paid implicitly), never overdue.
	TrackedFrom time.Time
	// DueNotifiedUntil — the due notice has counted the occurrences up to this day.
	DueNotifiedUntil *time.Time
	// AutoCreate — record the transaction by itself when an occurrence comes due;
	// AutoCreatedUntil is the last day it has handled.
	AutoCreate       bool
	AutoCreatedUntil *time.Time
	// PausedFrom — the day it was paused (or archived while active); resuming skips the
	// occurrences of the gap.
	PausedFrom *time.Time
}

type (
	Monthly        = recurrence.Monthly
	RecurrenceRule = recurrence.RecurrenceRule
	RecurrenceEnd  = recurrence.RecurrenceEnd
)

type Progress struct {
	Done  int  `json:"done"`
	Total *int `json:"total"`
}

// StatusCounts — Upcoming is only counted for a setting with an end (a no-end
// one has no end to count to); NotCreated / Archived are the old names of
// Due / Skipped.
type StatusCounts struct {
	Upcoming   int `json:"upcoming"`
	Due        int `json:"due"`
	Pending    int `json:"pending"`
	Done       int `json:"done"`
	Paid       int `json:"paid"`
	Skipped    int `json:"skipped"`
	NotCreated int `json:"not_created"`
	Archived   int `json:"archived"`
}

// ChainStep is one occurrence of an installment chain (a setting with an end).
type ChainStep struct {
	Slot   string `json:"slot"`
	Status string `json:"status"`
}

// Stats — the detail page's numbers: what was paid, what is left, the amounts
// of the recorded occurrences.
type Stats struct {
	PaidCount      int      `json:"paid_count"`
	PaidTotal      float64  `json:"paid_total"`
	RemainingTotal *float64 `json:"remaining_total"` // only for a setting with an end
	LastPeriod     *string  `json:"last_period"`     // slot of the latest recorded occurrence
	AvgAmount      *float64 `json:"avg_amount"`
	LastAmount     *float64 `json:"last_amount"`
	MaxAmount      *float64 `json:"max_amount"`
	Series         []Point  `json:"series"` // recorded occurrences by slot, newest 24
}

type Point struct {
	Slot   string  `json:"slot"`
	Date   string  `json:"date"`
	Amount float64 `json:"amount"`
}

// Window is the span an upcoming list covers (a setting without an end).
type Window struct {
	From     string  `json:"from"`
	To       string  `json:"to"`
	NextFrom *string `json:"next_from"`
}

type Actions struct {
	Pause   bool `json:"pause"`
	Resume  bool `json:"resume"`
	Archive bool `json:"archive"`
	Restore bool `json:"restore"`
	Extend  bool `json:"extend"`
}

type Recurring struct {
	ID          uuid.UUID          `json:"id"`
	Name        string             `json:"name"`
	Description *string            `json:"description"`
	Note        *string            `json:"note"`
	IconCode    *shared.IconCode   `json:"icon_code"`
	Type        string             `json:"type"`
	Amount      *float64           `json:"amount"` // null = a reminder with no amount yet
	Account     *shared.AccountRef `json:"account"`
	Kind       string  `json:"kind"`
	TrialUntil *string `json:"trial_until"`
	YearlyCost float64 `json:"yearly_cost"`
	// MonthlyAmount — what it costs (or brings) in an average month: amount × times per month; null
	// without an amount.
	MonthlyAmount *float64 `json:"monthly_amount"`
	SavedCount    int      `json:"saved_count"`
	PendingCount  int      `json:"pending_count"`
	NextAmount    *float64 `json:"next_amount"`
	CurrentOccurrence *Occurrence           `json:"current_occurrence"`
	ToAccount         *shared.AccountRef    `json:"to_account"`
	Category          *shared.CategoryRef   `json:"category"`
	Goal              *shared.SavingGoalRef `json:"goal"`
	Tags              []shared.TagRef       `json:"tags"`
	Rule              RecurrenceRule        `json:"rule"`
	StartDate         string                `json:"start_date"`
	End               RecurrenceEnd         `json:"end"`
	Status            string                `json:"status"`
	NextDate          *string               `json:"next_date"`
	DaysUntil         *int                  `json:"days_until"`
	Progress          Progress              `json:"progress"`
	EndingSoon        bool                  `json:"ending_soon"`
	Counts            StatusCounts          `json:"counts"`
	Likely    bool      `json:"likely"`
	EditedAny bool      `json:"edited_any"`
	Actions   Actions   `json:"actions"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// DueCount — occurrences due and not recorded (overdue + today).
	DueCount int `json:"due_count"`
	// AvgAmount — the average of the recorded amounts, else the rule's amount (null without one).
	AvgAmount *float64 `json:"avg_amount"`
	// Chain — an installment (a setting with an end, up to 60 occurrences): the status of every occurrence.
	Chain []ChainStep `json:"chain"`
	// AutoCreate — the transaction is recorded by itself when an occurrence comes due.
	AutoCreate bool `json:"auto_create"`
	// RecordedTxCount — recorded occurrences that have a transaction (for "apply to the N recorded tx?").
	RecordedTxCount int `json:"recorded_tx_count"`
}

type ListCounts struct {
	Current  int `json:"current"`
	Archived int `json:"archived"`
	All      int `json:"all"`
}

type RecurringList struct {
	Data       []Recurring `json:"data"`
	Counts     ListCounts  `json:"counts"`
	Summary    ListSummary `json:"summary"`
	Pagination shared.Page `json:"pagination"`
}

type LinkedTx struct {
	ID          uuid.UUID          `json:"id"`
	Description *string            `json:"description"`
	Account     *shared.AccountRef `json:"account"`
	Amount      *float64           `json:"amount"`
	Date        *string            `json:"date"`
}

// Item is one occurrence of a setting: a stored row (id set) or one computed
// from the rule (id null, stored false), addressed by rule_id + slot.
type Item struct {
	ID            *uuid.UUID              `json:"id"`
	RuleID        uuid.UUID               `json:"rule_id"`
	Slot          string                  `json:"slot"` // the occurrence's original date
	Stored        bool                    `json:"stored"`
	AutoCreated   bool                    `json:"auto_created"`
	NeedsAmount   bool                    `json:"needs_amount"`
	Recurring     *shared.RecurringRef    `json:"recurring"`
	Seq           *int                    `json:"seq"`
	Total         *int                    `json:"total"`
	Date          string                  `json:"date"`
	DaysUntil     int                     `json:"days_until"`
	Status        string                  `json:"status"`
	Edited        bool                    `json:"edited"`
	Manual        bool                    `json:"manual"`
	Type          string                  `json:"type"`
	Amount        *float64                `json:"amount"` // null = not known yet (a reminder)
	Account       *shared.AccountRef      `json:"account"`
	ToAccount     *shared.AccountRef      `json:"to_account"`
	Category      *shared.CategoryRef     `json:"category"`
	Goal          *shared.SavingGoalRef   `json:"goal"`
	Tags          []shared.TagRef         `json:"tags"`
	Description   *string                 `json:"description"`
	Note          *string                 `json:"note"`
	Splits        []pending.ResolvedSplit `json:"splits"`
	Project       *shared.EventRef        `json:"project"`
	PendingID     *uuid.UUID              `json:"pending_id"`
	Transaction   *LinkedTx               `json:"transaction"`
	SavingEntryID *uuid.UUID              `json:"saving_entry_id"`
	CreatedAt     *time.Time              `json:"created_at"` // null while computed
	UpdatedAt     *time.Time              `json:"updated_at"`
}

type ItemList struct {
	Data       []Item      `json:"data"`
	Pagination shared.Page `json:"pagination"`
	// Window — set for the upcoming list of a setting without an end (from / months).
	Window *Window `json:"window"`
}

type Detail struct {
	Recurring Recurring `json:"recurring"`
	Items     ItemList  `json:"items"`
	Upcoming ItemList `json:"upcoming"`
	Stats    Stats    `json:"stats"`
	// TxUpdate — set by an edit that sent also_update_tx.
	TxUpdate        *TxUpdateResult        `json:"tx_update"`
	DeletePreview   *DeletePreview         `json:"delete_preview"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
}

type DeleteResult struct {
	ID                      uuid.UUID              `json:"id"`
	DeletedTransactionCount int                    `json:"deleted_transaction_count"`
	AccountBalances         []shared.WalletBalance `json:"account_balances"`
}

type ItemWriteResult struct {
	Item            Item                   `json:"item"`
	Transaction     any                    `json:"transaction"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	Setting *Recurring `json:"setting"`
}

type ItemsWriteResult struct {
	Items           []Item                 `json:"items"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	Setting *Recurring `json:"setting"`
}

type ItemDeleteResult struct {
	ID              uuid.UUID              `json:"id"`
	TransactionID   *uuid.UUID             `json:"transaction_id"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	Setting *Recurring `json:"setting"`
}

type BatchResult struct {
	Items           []Item                 `json:"items"`
	DeletedIDs      []uuid.UUID            `json:"deleted_ids"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	Setting *Recurring `json:"setting"`
}

type Fields struct {
	Type        *string     `json:"type"`
	Amount      *float64    `json:"amount"`
	AccountID   *uuid.UUID  `json:"account_id"`
	ToAccountID *uuid.UUID  `json:"to_account_id"`
	CategoryID  *uuid.UUID  `json:"category_id"`
	GoalID      *uuid.UUID  `json:"goal_id"`
	Description *string     `json:"description"`
	Note        *string     `json:"note"`
	TagIDs      []uuid.UUID `json:"tag_ids"`
	Date        *string     `json:"date"`

	present map[string]bool
}

func (f *Fields) has(k string) bool { return f.present[k] }

func (f *Fields) values() bool {
	for _, k := range []string{"type", "amount", "account_id", "to_account_id", "category_id", "goal_id", "description", "note", "tag_ids"} {
		if f.has(k) {
			return true
		}
	}
	return false
}

type CreateRequest struct {
	Fields
	Name string `json:"name"`
	Kind       *string          `json:"kind"`
	TrialUntil *string          `json:"trial_until"`
	IconCode   *shared.IconCode `json:"icon_code"`
	Rule       *RecurrenceRule  `json:"rule"`
	StartDate  string           `json:"start_date"`
	End        *RecurrenceEnd   `json:"end"`
	// AutoCreate — record the transaction by itself when an occurrence comes due.
	AutoCreate *bool `json:"auto_create"`
}

// TxUpdateResult — what also_update_tx did to the transactions already recorded.
type TxUpdateResult struct {
	UpdatedCount int               `json:"updated_tx_count"`
	Failed       []TxUpdateFailure `json:"failed"`
}

type TxUpdateFailure struct {
	TransactionID uuid.UUID `json:"transaction_id"`
	Reason        string    `json:"reason"` // not_found | account_archived | transfer | refused
}

type UpdateRequest struct {
	Fields
	Name *string `json:"name"`
	Kind *string `json:"kind"`
	TrialUntil     *string          `json:"trial_until"`
	IconCode       *shared.IconCode `json:"icon_code"`
	Rule           *RecurrenceRule  `json:"rule"`
	StartDate      *string          `json:"start_date"`
	End            *RecurrenceEnd   `json:"end"`
	Scope          string           `json:"scope"`
	AlsoUpdateLink bool             `json:"also_update_link"`
	// AutoCreate — switch auto-create on / off (never back-creates).
	AutoCreate *bool `json:"auto_create"`
	// AlsoUpdateTx — push a change of name / category / wallet / tags / note to the
	// transactions already recorded from this setting (never date or amount).
	AlsoUpdateTx bool `json:"also_update_tx"`
}

type ExtendRequest struct {
	End *RecurrenceEnd `json:"end"`
}

type ItemCreateRequest struct {
	Fields
	RecurringID *uuid.UUID `json:"recurring_id"`
}

type ItemUpdateRequest struct {
	Fields
	Scope          string `json:"scope"`
	AlsoUpdateLink bool   `json:"also_update_link"`
}

type CreateNowRequest struct {
	Target string  `json:"target"`
	Fields *Fields `json:"fields"`
}

type BatchRequest struct {
	IDs            []uuid.UUID `json:"ids"`
	Action         string      `json:"action"`
	Target         string      `json:"target"`
	Fields         *Fields     `json:"fields"`
	AlsoUpdateLink bool        `json:"also_update_link"`
	DeleteLinkedTx bool        `json:"delete_linked_tx"`
}

type sourceRef struct {
	ItemID    uuid.UUID            `json:"item_id"`
	Recurring *shared.RecurringRef `json:"recurring"`
	Seq       *int                 `json:"seq"`
	Total     *int                 `json:"total"`
}

type DeletePreview struct {
	Items              int `json:"items"`               // all occurrences
	LinkedTransactions int `json:"linked_transactions"` // saved records that would go with delete_linked
	PendingDrafts      int `json:"pending_drafts"`      // drafts waiting in pending
}
