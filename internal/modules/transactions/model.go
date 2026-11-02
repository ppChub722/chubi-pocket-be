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
	ID          uuid.UUID  `json:"id"`
	UserID      uuid.UUID  `json:"user_id"`
	AccountID   *uuid.UUID `json:"-"` // nil = no wallet (floating)
	Type        TxType     `json:"type"`
	Amount      float64    `json:"amount"`
	CategoryID  *uuid.UUID `json:"-"`
	Date        string     `json:"date"` // YYYY-MM-DD; calendar date in user's tz
	Description *string    `json:"description"`
	Note        *string    `json:"note"`
	// PrivateNote (0.4.1-2 S11) — mine only, on a copy of a shared bill; never synced. Null on
	// another member's row.
	PrivateNote          *string    `json:"private_note"`
	TransferGroupID      *uuid.UUID `json:"transfer_group_id"`
	SourcePersonalDebtID *uuid.UUID `json:"-"` // the debt entry this row is the wallet side of (see Counterparty)
	// SplitTransactionID / SplitPersonID — the paper (split or event bill) this row is my book of,
	// and my person on it. MyShare — my part of that bill (what reports count); nil = the amount.
	SplitTransactionID *uuid.UUID `json:"-"`
	SplitPersonID      *uuid.UUID `json:"-"`
	MyShare            *float64   `json:"-"`
	// DebtConnected (0.4.1-1 D2) — an existing transaction connected to a person-book row later (it
	// can be disconnected); DebtPrevCategoryID is its category from before, restored then.
	DebtConnected      bool       `json:"-"`
	DebtPrevCategoryID *uuid.UUID `json:"-"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// Cash — what the row moves on its wallet.
func (t *Transaction) Cash() float64 { return t.Amount }

// Share — what the row counts in reports: my share of a bill, else the amount.
func (t *Transaction) Share() float64 {
	if t.MyShare != nil {
		return *t.MyShare
	}
	return t.Amount
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

type SetTagsRequest struct {
	TagIDs *[]uuid.UUID `json:"tag_ids" binding:"required"`
}

type TransferPeer struct {
	ID        uuid.UUID          `json:"id"`
	Direction string             `json:"direction"`
	Account   *shared.AccountRef `json:"account"` // nil = the twin has no wallet
}

type TransactionDetail struct {
	Transaction
	Currency     string              `json:"currency"`
	Account      *shared.AccountRef  `json:"account"`
	Category     *shared.CategoryRef `json:"category"`
	Tags         []shared.TagRef     `json:"tags"` // the caller's tags on the row
	Project      *shared.EventRef    `json:"project"`
	TransferPeer *TransferPeer       `json:"transfer_peer"` // transfer rows only
	Counterparty *shared.DebtLink    `json:"counterparty"`
	// SplitBill / EventBill — the paper this row is my book of (never both): a split bill, or a bill
	// of the event Project.
	SplitBill     *shared.PaperBookRef      `json:"split_bill"`
	EventBill     *shared.PaperBookRef      `json:"event_bill"`
	RecurringItem *shared.RecurringItemLink `json:"recurring_item"`
	MyShare       *float64                  `json:"my_share"`
	// WalletAmount — what the row moved on its wallet (0 with no wallet).
	WalletAmount    float64         `json:"wallet_amount"`
	CreatedBy       *shared.UserRef `json:"created_by"`
	IsLocked        bool            `json:"is_locked"`
	CanEditCategory bool            `json:"can_edit_category"`
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
	// PrivateNote (0.4.1-2 S11) — only on my book of a paper; null / "" clears.
	PrivateNote *string `json:"private_note"`

	privateNotePresent         bool
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
	_, r.privateNotePresent = probe["private_note"]
	return nil
}

// PrivateNoteChange — the private note sent (nil = clear) and whether it was sent at all.
func (r *UpdateRequest) PrivateNoteChange() (*string, bool) {
	return r.PrivateNote, r.privateNotePresent || r.PrivateNote != nil
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
