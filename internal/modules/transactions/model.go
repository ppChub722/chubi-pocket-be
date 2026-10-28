package transactions

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type TxType string

const (
	TypeExpense  TxType = "expense"
	TypeIncome   TxType = "income"
	TypeTransfer TxType = "transfer"
)

type Transaction struct {
	ID                         uuid.UUID  `json:"id"`
	UserID                     uuid.UUID  `json:"user_id"`
	AccountID                  *uuid.UUID `json:"-"` // nil = no wallet (floating)
	Type                       TxType     `json:"type"`
	Amount                     float64    `json:"amount"`
	CategoryID                 *uuid.UUID `json:"-"`
	Date                       string     `json:"date"` // YYYY-MM-DD; calendar date in user's tz
	Description                *string    `json:"description"`
	Note                       *string    `json:"note"`
	// PrivateNote (0.4.1-2 S11) — mine only, on a copy of a shared bill; never synced. Null on
	// another member's row.
	PrivateNote     *string    `json:"private_note"`
	TransferGroupID *uuid.UUID `json:"transfer_group_id"`
	SourcePersonalDebtID       *uuid.UUID `json:"-"` // the debt entry this row is the wallet side of (see Counterparty)
	SourceProjectTransactionID *uuid.UUID `json:"source_project_transaction_id"`
	ProjectID                  *uuid.UUID `json:"-"`
	// DebtConnected (0.4.1-1 D2) — an existing transaction connected to a person-book row later (it
	// can be disconnected); DebtPrevCategoryID is its category from before, restored then.
	DebtConnected      bool       `json:"-"`
	DebtPrevCategoryID *uuid.UUID `json:"-"`
	// PaidByAmount (0.4.0-3) — the part of this expense someone else paid (the bill stays Amount; the
	// wallet moves only Amount − PaidByAmount). nil = I paid it all.
	PaidByAmount *float64  `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Cash — what the row moves on its wallet: the amount minus the part someone else paid.
func (t *Transaction) Cash() float64 {
	if t.PaidByAmount == nil {
		return t.Amount
	}
	return t.Amount - *t.PaidByAmount
}

// Link modes (0.4.0-3): how the bill is shared, one at a time.
const (
	LinkPersonal = "personal" // all mine
	LinkSplit    = "split"    // I paid, others owe me their part (splits)
	LinkPaid     = "paid"     // someone else paid (part of) it for me (paid_by)
	LinkEvent    = "event"    // an event bill
)

// EventTarget — the event a row switches into, and the bill's people (default: I paid, no splits).
type EventTarget struct {
	ProjectID uuid.UUID `json:"project_id" binding:"required"`
	shared.BillPeople
}

// PersonDelta — a debt person's balance before / after a write (the preview's dry run).
type PersonDelta struct {
	Key    string           `json:"key"`
	Person shared.PersonRef `json:"person"`
	Before float64          `json:"before"`
	After  float64          `json:"after"`
	Delta  float64          `json:"delta"`
	// SideBefore / SideAfter — receivable | payable | clear; Flipped = the side changed from
	// receivable to payable or back (an overpaid settle).
	SideBefore string `json:"side_before"`
	SideAfter  string `json:"side_after"`
	Flipped    bool   `json:"flipped"`
}

// NewPersonDelta fills the delta and the sides from before / after.
func NewPersonDelta(key string, person shared.PersonRef, before, after float64) PersonDelta {
	d := PersonDelta{Key: key, Person: person, Before: round2(before), After: round2(after), Delta: round2(after - before),
		SideBefore: shared.DebtSide(before), SideAfter: shared.DebtSide(after)}
	d.Flipped = d.SideBefore != "clear" && d.SideAfter != "clear" && d.SideBefore != d.SideAfter
	return d
}

// WalletDelta — a wallet's balance before / after a write (the preview's dry run).
type WalletDelta struct {
	AccountID uuid.UUID `json:"account_id"`
	Before    float64   `json:"before"`
	After     float64   `json:"after"`
	Delta     float64   `json:"delta"`
}

// PreviewResult — PUT /transactions/{id}?preview=true: what the edit would do, nothing saved.
type PreviewResult struct {
	Preview bool                `json:"preview"`
	Rows    []TransactionDetail `json:"rows"`
	People  []PersonDelta       `json:"people"`
	Wallets []WalletDelta       `json:"wallets"`
}

// PaidByInput — who paid (part of) the bill for me: a contact, or a typed name (person_name), and
// how much they paid (≤ the bill; the rest is mine from the wallet).
type PaidByInput struct {
	ContactID  *uuid.UUID `json:"contact_id"`
	PersonName string     `json:"person_name" binding:"omitempty,max=100"`
	Amount     float64    `json:"amount"      binding:"required,gt=0"`
}

// PaidBy — the paid_by view of a row: the debt "I owe them" it made, and the cash split.
type PaidBy struct {
	DebtID uuid.UUID        `json:"debt_id"`
	Key    string           `json:"key"`
	Person shared.PersonRef `json:"person"`
	Amount float64          `json:"amount"`  // what they paid
	MyPaid float64          `json:"my_paid"` // what I paid from the wallet (0 = no wallet)
	// Source — me (I recorded it) | partner (another user's split on me, mirrored into my book: its
	// money is locked).
	Source string `json:"source"`
	Paid   bool   `json:"paid"` // the debt's "จ่ายแล้ว" label
}

type SplitInput struct {
	PersonName string     `json:"person_name" binding:"required,min=1,max=100"`
	ContactID  *uuid.UUID `json:"contact_id"  binding:"omitempty"`
	OwedAmount float64    `json:"owed_amount" binding:"required,gt=0"`
}

type SplitEdit struct {
	DebtID     *uuid.UUID `json:"debt_id"`
	PersonName string     `json:"person_name" binding:"omitempty,max=100"`
	ContactID  *uuid.UUID `json:"contact_id"`
	OwedAmount float64    `json:"owed_amount" binding:"required,gt=0"`

	ContactIDSet bool `json:"-"`
}

func (e *SplitEdit) UnmarshalJSON(data []byte) error {
	type alias SplitEdit
	if err := json.Unmarshal(data, (*alias)(e)); err != nil {
		return err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	_, e.ContactIDSet = probe["contact_id"]
	return nil
}

type EditSplitsRequest struct {
	Splits *[]SplitEdit `json:"splits" binding:"required,dive"`
}

type SetTagsRequest struct {
	TagIDs *[]uuid.UUID `json:"tag_ids" binding:"required"`
}

type SplitRef struct {
	DebtID      uuid.UUID        `json:"debt_id"`
	Key         string           `json:"key"`
	Person      shared.PersonRef `json:"person"`
	DisplayName string           `json:"person_name"`
	Direction   string           `json:"direction"` // paid | received
	Amount      float64          `json:"amount"`
	Paid        bool             `json:"paid"`

	PersonName string     `json:"-"`
	ContactID  *uuid.UUID `json:"-"`
}

type TransferPeer struct {
	ID        uuid.UUID          `json:"id"`
	Direction string             `json:"direction"`
	Account   *shared.AccountRef `json:"account"` // nil = the twin has no wallet
}

type TransactionDetail struct {
	Transaction
	Currency      string                    `json:"currency"`
	Account       *shared.AccountRef        `json:"account"`
	Category      *shared.CategoryRef       `json:"category"`
	Tags          []shared.TagRef           `json:"tags"` // the caller's tags on the row
	Project       *shared.EventRef          `json:"project"`
	TransferPeer  *TransferPeer             `json:"transfer_peer"` // transfer rows only
	Counterparty  *shared.DebtLink          `json:"counterparty"`
	EventBill     *EventBillLink            `json:"event_bill"`
	RecurringItem *shared.RecurringItemLink `json:"recurring_item"`
	MyShare       *float64                  `json:"my_share"`
	SplitCount    int                       `json:"split_count"`
	Splits        *[]SplitRef               `json:"splits"`
	// LinkMode — personal | split | paid | event (the quick-create view the row was saved with).
	LinkMode string  `json:"link_mode"`
	PaidBy   *PaidBy `json:"paid_by"`
	// WalletAmount — what the row moved on its wallet (amount − paid_by.amount).
	WalletAmount    float64         `json:"wallet_amount"`
	CanSplit        bool            `json:"can_split"`
	CanEditSplits   bool            `json:"can_edit_splits"`
	CanJoinEvent    bool            `json:"can_join_event"`
	CreatedBy       *shared.UserRef `json:"created_by"`
	IsLocked        bool            `json:"is_locked"`
	CanEditCategory bool            `json:"can_edit_category"`

	HasSplits bool `json:"-"`
}

type EventBillLink struct {
	Event  shared.EventRef `json:"event"`
	BillID uuid.UUID       `json:"bill_id"`
	Linked bool            `json:"linked"`
	// Payer / BillAmount / Members (0.4.0-3, detail and write answers; null / 0 / null on list rows):
	// who paid the bill and each member's share (the payer first, with the rest of the bill).
	Payer      *shared.MemberRef  `json:"payer"`
	BillAmount float64            `json:"bill_amount"`
	Members    []shared.BillShare `json:"members"`
	// Kind / InSync / Differences (0.4.0-3 B3, detail and write answers, live): origin = the bill was
	// made from this transaction (its amount is the bill), copy = my share of someone's bill. My book
	// and the event book never change each other by themselves; they may differ.
	Kind        string                  `json:"kind"`
	InSync      *bool                   `json:"in_sync"`
	Differences []shared.BillDifference `json:"differences"`
	// EditInBill (0.4.1-2 SA1) — "edit" opens the bill form: a connected copy, the event not
	// completed. PendingUpdate (S8) — an unanswered notice about the bill waits.
	EditInBill    bool `json:"edit_in_bill"`
	PendingUpdate bool `json:"pending_update"`
}

type TransferResponse struct {
	TransferGroupID uuid.UUID           `json:"transfer_group_id"`
	Rows            []TransactionDetail `json:"rows"`
}

type WriteResult struct {
	Rows            []TransactionDetail    `json:"rows"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	// The debt person's new balance when the row is a debt entry's wallet side (a
	// personal_debts.DebtPerson; null otherwise).
	PersonBalance any `json:"person_balance"`
	// PersonBalances (0.4.0-3 B3) — every person whose balance the write moved (a link-mode switch can
	// touch several: the split people, the payer). Omitted when none.
	PersonBalances []any `json:"person_balances,omitempty"`
	// Effects (0.4.1-1 B2) — everything the write changed, linked sides included.
	Effects shared.Effects `json:"effects"`
}

type DeleteResult struct {
	ID              uuid.UUID              `json:"id"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	DebtEntryID     *uuid.UUID             `json:"debt_entry_id"`
	PersonBalance   any                    `json:"person_balance"`
	RecurringItemID *uuid.UUID             `json:"recurring_item_id"`
	BillID          *uuid.UUID             `json:"bill_id"`
	Effects         shared.Effects         `json:"effects"` // 0.4.1-1 B2
}

type CreateRequest struct {
	Type                TxType      `json:"type"                    binding:"required,oneof=expense income transfer"`
	AccountID           *uuid.UUID  `json:"account_id"              binding:"omitempty"` // nil = no wallet; transfer still requires account (service validates)
	Amount              float64     `json:"amount"                  binding:"required,gt=0"`
	CategoryID          *uuid.UUID  `json:"category_id"             binding:"omitempty"`
	Date                string      `json:"date"                    binding:"required,datetime=2006-01-02"`
	Description         *string     `json:"description"             binding:"omitempty,max=200"`
	Note                *string     `json:"note"                    binding:"omitempty,max=500"`
	TransferToAccountID *uuid.UUID  `json:"transfer_to_account_id"  binding:"omitempty"`
	TagIDs              []uuid.UUID `json:"tag_ids" binding:"omitempty"`
	// AllowZero — system callers only (recurring auto-create, 0.4.1-1 R12): amount 0 is recorded.
	AllowZero bool `json:"-"`

	Splits                     []SplitInput `json:"splits"                         binding:"omitempty,dive"`
	PaidBy                     *PaidByInput `json:"paid_by"                        binding:"omitempty"`
	MyShare                    *float64     `json:"my_share"                       binding:"omitempty,gt=0"`
	ProjectID                  *uuid.UUID   `json:"project_id"                     binding:"omitempty"`
	SourceProjectTransactionID *uuid.UUID   `json:"source_project_transaction_id"  binding:"omitempty"`
}

type UpdateRequest struct {
	Amount              *float64    `json:"amount"                  binding:"omitempty,gt=0"`
	Date                *string     `json:"date"                    binding:"omitempty,datetime=2006-01-02"`
	CategoryID          *uuid.UUID  `json:"category_id"             binding:"omitempty"`
	Description         *string     `json:"description"             binding:"omitempty,max=200"`
	Note                *string     `json:"note"                    binding:"omitempty,max=500"`
	AccountID           *uuid.UUID  `json:"account_id"              binding:"omitempty"`
	TransferToAccountID *uuid.UUID  `json:"transfer_to_account_id"  binding:"omitempty"`
	TagIDs              []uuid.UUID `json:"tag_ids"                 binding:"omitempty"`
	AlsoUpdateLink      bool        `json:"also_update_link"`
	// PaidBy — absent = unchanged; null = nobody paid for me any more (the debt goes); an object
	// sets / replaces who paid and how much.
	PaidBy *PaidByInput `json:"paid_by" binding:"omitempty"`
	// LinkMode (0.4.0-3 B3) — switch how the bill is shared: personal | split | paid | event. The
	// other modes' data goes (splits / the paid-by debt deleted, the event left); the new mode takes
	// splits / paid_by / event from this request. Absent = keep the mode (splits may still be sent
	// to edit a split row).
	LinkMode *string `json:"link_mode" binding:"omitempty,oneof=personal split paid event"`
	// Splits — replaces the row's splits (as PUT /transactions/{id}/splits).
	Splits *[]SplitEdit `json:"splits" binding:"omitempty,dive"`
	// Event — link_mode event: the event and the bill's payer / members.
	Event *EventTarget `json:"event" binding:"omitempty"`
	// ApplyToEvent — "แก้ในอีเวนต์ด้วย": the row's amount / date / description / note (and an
	// origin's tags) go to its event bill. also_update_link does the same (and reaches a recurring
	// item).
	ApplyToEvent bool `json:"apply_to_event"`
	// PrivateNote (0.4.1-2 S11) — only on a copy of a shared bill; null / "" clears.
	PrivateNote *string `json:"private_note"`

	privateNotePresent         bool
	paidByPresent              bool
	categoryIDPresent          bool
	descriptionPresent         bool
	notePresent                bool
	accountIDPresent           bool
	transferToAccountIDPresent bool
	tagIDsPresent              bool
}

func (r *UpdateRequest) UnmarshalJSON(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	type alias UpdateRequest
	if err := json.Unmarshal(data, (*alias)(r)); err != nil {
		return err
	}
	_, r.categoryIDPresent = probe["category_id"]
	_, r.descriptionPresent = probe["description"]
	_, r.notePresent = probe["note"]
	_, r.accountIDPresent = probe["account_id"]
	_, r.transferToAccountIDPresent = probe["transfer_to_account_id"]
	_, r.tagIDsPresent = probe["tag_ids"]
	_, r.paidByPresent = probe["paid_by"]
	_, r.privateNotePresent = probe["private_note"]
	return nil
}

// PrivateNoteChange — the private note sent (nil = clear) and whether it was sent at all.
func (r *UpdateRequest) PrivateNoteChange() (*string, bool) {
	return r.PrivateNote, r.privateNotePresent || r.PrivateNote != nil
}

// PaidByChange — the paid_by sent (nil = remove it) and whether it was sent at all.
func (r *UpdateRequest) PaidByChange() (*PaidByInput, bool) {
	return r.PaidBy, r.paidByPresent || r.PaidBy != nil
}

// moneyChange — the request touches the row's money (what a partner's mirrored split locks).
func (r *UpdateRequest) moneyChange() bool {
	_, acc := r.AccountIDChange()
	_, pb := r.PaidByChange()
	return r.Amount != nil || r.Date != nil || acc || pb || r.LinkMode != nil || r.Splits != nil || r.Event != nil
}

func (r *UpdateRequest) CategoryIDChange() (*uuid.UUID, bool) {
	return r.CategoryID, r.categoryIDPresent || r.CategoryID != nil
}

func (r *UpdateRequest) DescriptionChange() (*string, bool) {
	return shared.CleanText(r.Description), r.descriptionPresent || r.Description != nil
}

func (r *UpdateRequest) NoteChange() (*string, bool) {
	return shared.CleanText(r.Note), r.notePresent || r.Note != nil
}

func (r *UpdateRequest) SetText(description, note *string) {
	r.Description, r.Note = description, note
	r.descriptionPresent, r.notePresent = true, true
}

func (r *UpdateRequest) MarkPresent(keys ...string) {
	for _, k := range keys {
		switch k {
		case "category_id":
			r.categoryIDPresent = true
		case "description":
			r.descriptionPresent = true
		case "note":
			r.notePresent = true
		case "account_id":
			r.accountIDPresent = true
		case "transfer_to_account_id":
			r.transferToAccountIDPresent = true
		case "tag_ids":
			r.tagIDsPresent = true
		case "paid_by":
			r.paidByPresent = true
		}
	}
}

func (r *UpdateRequest) AccountIDChange() (*uuid.UUID, bool) {
	return r.AccountID, r.accountIDPresent || r.AccountID != nil
}

func (r *UpdateRequest) TransferToAccountIDChange() (*uuid.UUID, bool) {
	return r.TransferToAccountID, r.transferToAccountIDPresent
}

func (r *UpdateRequest) TagIDsChange() ([]uuid.UUID, bool) {
	return r.TagIDs, r.tagIDsPresent || r.TagIDs != nil
}

type ListFilter struct {
	AccountID  *uuid.UUID
	CategoryID *uuid.UUID
	Type       *TxType
	From       *string // YYYY-MM-DD
	To         *string
	NoWallet   bool

	AccountIDs           []uuid.UUID // rows on ANY of these wallets
	CategoryIDs          []uuid.UUID // rows in ANY of these categories
	Types                []TxType    // rows of ANY of these types
	NoTags               bool        // rows with no tag (OR-ed with TagIDs)
	AmountMin, AmountMax *float64
	TZ                   string
	RangeFrom, RangeTo   time.Time
	// IncludeArchived — rows on archived wallets are hidden unless asked for (or when listing that
	// wallet itself via AccountID).
	IncludeArchived bool
	Q               string
	TagIDs          []uuid.UUID // rows having ANY of these tags
	IncludeChildren bool        // with CategoryID: also its subcategories
	Uncategorized   bool        // only rows with no category
	CanJoinEvent    bool
	CounterpartyKey string
	Page            int
	PerPage         int
	Sort            string // date_desc | date_asc | amount_desc | amount_asc
}

type Totals struct {
	Income  float64 `json:"income"`
	Expense float64 `json:"expense"`
	Net     float64 `json:"net"`
	Count   int     `json:"count"`
}

type ListResponse struct {
	Data          []TransactionDetail `json:"data"`
	Pagination    shared.Page         `json:"pagination"`
	Totals        Totals              `json:"totals"`
	AvgDailySpend float64             `json:"avg_daily_spend"`
	Daily         []DailyPoint        `json:"daily"`
}

type SummaryRequest struct {
	From       string     // required
	To         string     // required
	AccountID  *uuid.UUID // optional
	CategoryID *uuid.UUID
	Type       *TxType // optional: income | expense — filters rows before grouping
	GroupBy    string  // empty | day | week | month | category | parent_category | account
	// GroupsOnly skips the totals read: the caller uses only Groups.
	GroupsOnly bool
}

type SummaryGroup struct {
	Key      string              `json:"key"`
	Name     string              `json:"name"`
	Account  *shared.AccountRef  `json:"account"`
	Category *shared.CategoryRef `json:"category"`
	Total    float64             `json:"total"`
	Count    int                 `json:"count"`
	Income   float64             `json:"income"`
	Expense  float64             `json:"expense"`
}

// SummaryResponse — spec schema TransactionSummary (groups is [] without group_by).
type SummaryResponse struct {
	From             string         `json:"from"`
	To               string         `json:"to"`
	Currency         string         `json:"currency"`
	TotalIncome      float64        `json:"total_income"`
	TotalExpense     float64        `json:"total_expense"`
	Net              float64        `json:"net"`
	TransactionCount int            `json:"transaction_count"`
	Groups           []SummaryGroup `json:"groups"`
}
