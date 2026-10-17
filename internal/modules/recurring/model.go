package recurring

// Recurring (0.3.1-10, owner 2026-10-13 FINAL; replaces scheduled). Two parts:
//   - a SETTING (recurring_rules) = the generator: a Google-Calendar-style
//     rule + the values of what it produces;
//   - its ITEMS (recurring_items) = the module's own records, one per date
//     (+ one-off upcoming items from a future-dated POST /transactions, and
//     manual items), LINKED to a pending draft or a real transaction the
//     same way an event bill links its transaction. Status comes from the
//     link; cross-link edits never notify, writes take also_update_link.
// The due check (no job runner) runs inside the boot reads: items dated
// today or earlier of ACTIVE settings (and one-offs) → one pending draft
// each, exactly once; one grouped recurring_due notice.

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const dateLayout = "2006-01-02"

// Item statuses (derived, see statusSQL).
const (
	StatusUpcoming   = "upcoming"
	StatusNotCreated = "not_created"
	StatusPending    = "pending"
	StatusDone       = "done"
	StatusArchived   = "archived"
)

// statusSQL — an item's status from its link / flags (alias i).
const statusSQL = `(CASE WHEN i.transaction_id IS NOT NULL OR i.saving_entry_id IS NOT NULL THEN 'done'
	WHEN i.pending_id IS NOT NULL THEN 'pending'
	WHEN i.archived THEN 'archived'
	WHEN i.produced_at IS NULL THEN 'upcoming'
	ELSE 'not_created' END)`

// Rule — the recurring_rules row.
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
	Freq           string
	Interval       int
	Weekdays       []int
	MonthlyMode    *string
	MonthDay       *int
	MonthNth       *int
	MonthWeekday   *int
	Dates          []time.Time
	StartDate      time.Time
	EndKind        string
	EndDate        *time.Time
	EndCount       *int
	TotalCount     *int
	Kind           string     // subscription | installment | income | reminder
	TrialUntil     *time.Time // subscription: the free trial runs until
	GeneratedUntil *time.Time
	Status         string
	EndingNotified bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// --- API shapes (spec schemas Recurring*) ---

type Monthly struct {
	Mode    string `json:"mode"`
	Day     *int   `json:"day"`
	Nth     *int   `json:"nth"`
	Weekday *int   `json:"weekday"`
}

// RecurrenceRule — spec schema RecurrenceRule.
type RecurrenceRule struct {
	Freq     string   `json:"freq"`
	Interval int      `json:"interval"`
	Weekdays []int    `json:"weekdays"`
	Monthly  *Monthly `json:"monthly"`
	Dates    []string `json:"dates"`
}

// RecurrenceEnd — spec schema RecurrenceEnd.
type RecurrenceEnd struct {
	Kind  string  `json:"kind"`
	Date  *string `json:"date"`
	Count *int    `json:"count"`
}

type Progress struct {
	Done  int  `json:"done"`
	Total *int `json:"total"`
}

type StatusCounts struct {
	Upcoming   int `json:"upcoming"`
	NotCreated int `json:"not_created"`
	Pending    int `json:"pending"`
	Done       int `json:"done"`
	Archived   int `json:"archived"`
}

type Actions struct {
	Pause   bool `json:"pause"`
	Resume  bool `json:"resume"`
	Archive bool `json:"archive"`
	Restore bool `json:"restore"`
	Extend  bool `json:"extend"`
}

// Recurring — spec schema Recurring.
type Recurring struct {
	ID          uuid.UUID          `json:"id"`
	Name        string             `json:"name"`
	Description *string            `json:"description"`
	Note        *string            `json:"note"`
	IconCode    *shared.IconCode   `json:"icon_code"`
	Type        string             `json:"type"`
	Amount      *float64           `json:"amount"` // null = a reminder with no amount yet
	Account     *shared.AccountRef `json:"account"`
	// 0.4.0 (spec 11): kind, trial_until, yearly_cost, and the detail numbers.
	Kind       string  `json:"kind"`
	TrialUntil *string `json:"trial_until"`
	YearlyCost float64 `json:"yearly_cost"`
	// MonthlyAmount — what it costs (or brings) in an average month: amount × times per month; null without an amount.
	MonthlyAmount *float64 `json:"monthly_amount"`
	SavedCount    int      `json:"saved_count"`
	PendingCount  int      `json:"pending_count"`
	NextAmount    *float64 `json:"next_amount"`
	// CurrentOccurrence — the present period's occurrence (not done / skipped): what a transaction links to. null = none.
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
	// Likely — only with the picker hints (for_type + category_id / amount): the setting matches the transaction by category or amount (±5%); such settings come first.
	Likely    bool      `json:"likely"`
	EditedAny bool      `json:"edited_any"`
	Actions   Actions   `json:"actions"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ListCounts struct {
	Current  int `json:"current"`
	Archived int `json:"archived"`
	All      int `json:"all"`
}

// RecurringList — spec schema RecurringList.
type RecurringList struct {
	Data       []Recurring `json:"data"`
	Counts     ListCounts  `json:"counts"`
	Summary    ListSummary `json:"summary"`
	Pagination shared.Page `json:"pagination"`
}

// LinkedTx — spec schema LinkedTx.
type LinkedTx struct {
	ID          uuid.UUID          `json:"id"`
	Description *string            `json:"description"`
	Account     *shared.AccountRef `json:"account"`
}

// Item — spec schema RecurringItem.
type Item struct {
	ID            uuid.UUID               `json:"id"`
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
	CreatedAt     time.Time               `json:"created_at"`
	UpdatedAt     time.Time               `json:"updated_at"`
}

// ItemList — spec schema RecurringItemList.
type ItemList struct {
	Data       []Item      `json:"data"`
	Pagination shared.Page `json:"pagination"`
}

// Detail — spec schema RecurringDetail.
type Detail struct {
	Recurring Recurring `json:"recurring"`
	Items     ItemList  `json:"items"`
	// Upcoming — the first page of the upcoming items (the detail page is one call).
	Upcoming ItemList `json:"upcoming"`
	// DeletePreview — only with ?include=delete_preview, else null.
	DeletePreview   *DeletePreview         `json:"delete_preview"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
}

// DeleteResult — spec schema RecurringDeleteResult.
type DeleteResult struct {
	ID                      uuid.UUID              `json:"id"`
	DeletedTransactionCount int                    `json:"deleted_transaction_count"`
	AccountBalances         []shared.WalletBalance `json:"account_balances"`
}

// ItemWriteResult — spec schema RecurringItemWriteResult.
type ItemWriteResult struct {
	Item            Item                   `json:"item"`
	Transaction     any                    `json:"transaction"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	// Setting — the owning setting after the write (null when the items belong to no / several settings).
	Setting *Recurring `json:"setting"`
}

// ItemsWriteResult — spec schema RecurringItemsWriteResult.
type ItemsWriteResult struct {
	Items           []Item                 `json:"items"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	// Setting — the owning setting after the write (null when the items belong to no / several settings).
	Setting *Recurring `json:"setting"`
}

// ItemDeleteResult — spec schema RecurringItemDeleteResult.
type ItemDeleteResult struct {
	ID              uuid.UUID              `json:"id"`
	TransactionID   *uuid.UUID             `json:"transaction_id"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	// Setting — the owning setting after the write (null when the items belong to no / several settings).
	Setting *Recurring `json:"setting"`
}

// BatchResult — spec schema RecurringItemBatchResult.
type BatchResult struct {
	Items           []Item                 `json:"items"`
	DeletedIDs      []uuid.UUID            `json:"deleted_ids"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	// Setting — the owning setting after the write (null when the items belong to no / several settings).
	Setting *Recurring `json:"setting"`
}

// --- requests ---

// Fields — spec schema RecurringFields (presence-tracked: absent = unchanged,
// null clears a nullable one).
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

// values — any value field set (the date is not a value field).
func (f *Fields) values() bool {
	for _, k := range []string{"type", "amount", "account_id", "to_account_id", "category_id", "goal_id", "description", "note", "tag_ids"} {
		if f.has(k) {
			return true
		}
	}
	return false
}

// CreateRequest — spec schema RecurringCreateRequest.
type CreateRequest struct {
	Fields
	Name string `json:"name"`
	// Kind — subscription / installment / income / reminder; omitted = derived from type (+ end).
	Kind       *string          `json:"kind"`
	TrialUntil *string          `json:"trial_until"`
	IconCode   *shared.IconCode `json:"icon_code"`
	Rule       *RecurrenceRule  `json:"rule"`
	StartDate  string           `json:"start_date"`
	End        *RecurrenceEnd   `json:"end"`
}

// UpdateRequest — spec schema RecurringUpdateRequest.
type UpdateRequest struct {
	Fields
	Name *string `json:"name"`
	Kind *string `json:"kind"`
	// TrialUntil — a date sets it, "" clears it, absent leaves it.
	TrialUntil     *string          `json:"trial_until"`
	IconCode       *shared.IconCode `json:"icon_code"`
	Rule           *RecurrenceRule  `json:"rule"`
	StartDate      *string          `json:"start_date"`
	End            *RecurrenceEnd   `json:"end"`
	Scope          string           `json:"scope"`
	AlsoUpdateLink bool             `json:"also_update_link"`
}

// ExtendRequest — spec schema RecurringExtendRequest.
type ExtendRequest struct {
	End *RecurrenceEnd `json:"end"`
}

// ItemCreateRequest — spec schema RecurringItemCreateRequest.
type ItemCreateRequest struct {
	Fields
	RecurringID *uuid.UUID `json:"recurring_id"`
}

// ItemUpdateRequest — spec schema RecurringItemUpdateRequest.
type ItemUpdateRequest struct {
	Fields
	Scope          string `json:"scope"`
	AlsoUpdateLink bool   `json:"also_update_link"`
}

// CreateNowRequest — spec schema RecurringItemCreateNowRequest.
type CreateNowRequest struct {
	Target string  `json:"target"`
	Fields *Fields `json:"fields"`
}

// BatchRequest — spec schema RecurringItemBatchRequest.
type BatchRequest struct {
	IDs            []uuid.UUID `json:"ids"`
	Action         string      `json:"action"`
	Target         string      `json:"target"`
	Fields         *Fields     `json:"fields"`
	AlsoUpdateLink bool        `json:"also_update_link"`
	DeleteLinkedTx bool        `json:"delete_linked_tx"`
}

// sourceRef — a produced draft's source_ref (the pending source chip).
type sourceRef struct {
	ItemID    uuid.UUID            `json:"item_id"`
	Recurring *shared.RecurringRef `json:"recurring"`
	Seq       *int                 `json:"seq"`
	Total     *int                 `json:"total"`
}

// DeletePreview — what deleting the setting touches. Linked records stay
// unless the delete asks for them to go too (delete_linked).
type DeletePreview struct {
	Items              int `json:"items"`               // all occurrences
	LinkedTransactions int `json:"linked_transactions"` // saved records that would go with delete_linked
	PendingDrafts      int `json:"pending_drafts"`      // drafts waiting in pending
}
