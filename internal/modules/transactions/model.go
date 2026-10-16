package transactions

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// TxType is the discriminator for the `type` enum.
type TxType string

const (
	TypeExpense  TxType = "expense"
	TypeIncome   TxType = "income"
	TypeTransfer TxType = "transfer"
)

// Transaction is the DB row. AccountID / CategoryID / ProjectID are not sent
// as-is (API spec v2): the response carries the resolved refs `account`,
// `category`, `project` instead (TransactionDetail).
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
	TransferGroupID            *uuid.UUID `json:"transfer_group_id"`
	SourcePersonalDebtID       *uuid.UUID `json:"-"` // the debt entry this row is the wallet side of (see Counterparty)
	SourceProjectTransactionID *uuid.UUID `json:"source_project_transaction_id"`
	ProjectID                  *uuid.UUID `json:"-"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
}

// SplitInput is the per-debtor row inside POST /v1/transactions{splits:[...]}.
// Each entry materializes as one personal_debts row on the splitter's side
// (direction='owed_to_me') + one mirror row on each linked debtor's side
// (direction='i_owe'). See design/spec/12-personal-debts.md. With a contact
// the shown name follows the contact; PersonName is only the stored fallback.
type SplitInput struct {
	PersonName string     `json:"person_name" binding:"required,min=1,max=100"`
	ContactID  *uuid.UUID `json:"contact_id"  binding:"omitempty"`
	OwedAmount float64    `json:"owed_amount" binding:"required,gt=0"`
}

// SplitEdit — one row of PUT /v1/transactions/:id/splits. With debt_id it
// keeps / re-amounts that existing split; contact_id on it switches / links
// the contact ("contact_id": null back to a free name); person_name renames
// a free-name split only — on a split with a contact it is ignored (the name
// follows the contact; Service.EditSplits rewrites it before the hook runs).
// Left out = unchanged. Without debt_id it adds a person (same fields as
// SplitInput).
type SplitEdit struct {
	DebtID     *uuid.UUID `json:"debt_id"`
	PersonName string     `json:"person_name" binding:"omitempty,max=100"`
	ContactID  *uuid.UUID `json:"contact_id"`
	OwedAmount float64    `json:"owed_amount" binding:"required,gt=0"`

	// ContactIDSet — the body carried "contact_id" (null included), so a
	// kept split's contact is being set or dropped. A non-nil ContactID
	// implies it.
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

// EditSplitsRequest — the WHOLE new list; a split left out is removed, []
// removes them all.
type EditSplitsRequest struct {
	Splits *[]SplitEdit `json:"splits" binding:"required,dive"`
}

// SetTagsRequest — PUT /v1/transactions/:id/tags: the caller's whole new tag
// set for the row ([] clears it).
type SetTagsRequest struct {
	TagIDs *[]uuid.UUID `json:"tag_ids" binding:"required"`
}

// SplitRef — one split entry a bill made in its author's debt ledger (spec
// schema Split). Person is resolved by the one person-name rule
// (shared.PersonNameSQL); DisplayName (JSON person_name) = Person.Name.
type SplitRef struct {
	DebtID      uuid.UUID        `json:"debt_id"`
	Key         string           `json:"key"`
	Person      shared.PersonRef `json:"person"`
	DisplayName string           `json:"person_name"`
	Direction   string           `json:"direction"` // paid | received
	Amount      float64          `json:"amount"`
	Paid        bool             `json:"paid"`

	// PersonName — the stored snapshot (counterparty_person_name); ContactID
	// — the stored contact. Go-side only (the response shows Person).
	PersonName string     `json:"-"`
	ContactID  *uuid.UUID `json:"-"`
}

// TransferPeer — the other row of a transfer (spec schema TransferPeer).
// Direction is THIS row's direction (out = the money leaves this wallet).
type TransferPeer struct {
	ID        uuid.UUID          `json:"id"`
	Direction string             `json:"direction"`
	Account   *shared.AccountRef `json:"account"` // nil = the twin has no wallet
}

// TransactionDetail is the page-ready row (spec schema Transaction): the
// same shape in lists, detail and write responses. Built by buildDetails.
type TransactionDetail struct {
	Transaction
	// The wallet's currency, else the author's (users.currency).
	Currency string             `json:"currency"`
	Account  *shared.AccountRef `json:"account"`
	// The AUTHOR's category (on a shared wallet other members see it
	// read-only; see CanEditCategory).
	Category     *shared.CategoryRef `json:"category"`
	Tags         []shared.TagRef     `json:"tags"` // the caller's tags on the row
	Project      *shared.EventRef    `json:"project"`
	TransferPeer *TransferPeer       `json:"transfer_peer"` // transfer rows only
	// The debt entry this row is the wallet side of (one record: an edit /
	// delete of either changes both). Shown to the row's author only.
	Counterparty *shared.DebtLink `json:"counterparty"`
	// The event bill the row belongs to (tx detail → the bill).
	EventBill *EventBillLink `json:"event_bill"`
	// The recurring item linked to the row (its author only).
	RecurringItem *shared.RecurringItemLink `json:"recurring_item"`
	// The author's share (spec 12 §4.5, shared.ShareAmountExpr): amount −
	// what others owe on it (split debts, an event row's member splits).
	// Expense / income only (nil = null for transfers); what reports and
	// budgets count.
	MyShare *float64 `json:"my_share"`
	// The debts this row made in its author's book.
	SplitCount int `json:"split_count"`
	// The caller's split debts on the row — detail and write responses
	// only (nil = null on list rows; [] when none).
	Splits *[]SplitRef `json:"splits"`
	// What the caller may do with the row's splits / events (the BE's own
	// rules, so the client doesn't re-derive them):
	//   can_split       — the row can carry personal splits at all: expense /
	//                     income, not a repayment or system row. An event
	//                     bill too — its own splits are a layer on my share
	//                     of it (Σ ≤ amount − the board's member splits).
	//   can_edit_splits — can_split and the caller is the author
	//                     (PUT /:id/splits).
	//   can_join_event  — the caller may pull it into an event (POST
	//                     /projects/:id/bills, /projects/quick; with move if
	//                     it's already in one): author, expense / income, not
	//                     a repayment or system row, no split repaid or
	//                     forgiven (an event bill's own splits don't move, so
	//                     they never block).
	CanSplit      bool `json:"can_split"`
	CanEditSplits bool `json:"can_edit_splits"`
	CanJoinEvent  bool `json:"can_join_event"`
	// Shared wallets (spec §14): created_by is set on rows of a wallet with
	// more than one active member. is_locked — the row sits on a wallet the
	// caller is no longer an active member of (read-only for them).
	// can_edit_category — the caller is the author, the row isn't locked and
	// its category is a user one (expense / income, not a repayment, system
	// or transfer row).
	CreatedBy       *shared.UserRef `json:"created_by"`
	IsLocked        bool            `json:"is_locked"`
	CanEditCategory bool            `json:"can_edit_category"`

	// HasSplits — SplitCount > 0 (Go-side convenience).
	HasSplits bool `json:"-"`
}

// EventBillLink — spec schema EventBillLink. Linked = the bill was made from
// this row (shared.EventBillPredicate: money shared); false = a personal copy
// of a share.
type EventBillLink struct {
	Event  shared.EventRef `json:"event"`
	BillID uuid.UUID       `json:"bill_id"`
	Linked bool            `json:"linked"`
}

// TransferResponse — both rows of a transfer (OUT first, IN second), as
// Create / Update return them to Go callers. The HTTP responses use
// WriteResult.
type TransferResponse struct {
	TransferGroupID uuid.UUID           `json:"transfer_group_id"`
	Rows            []TransactionDetail `json:"rows"`
}

// WriteResult — what a transaction write returns over HTTP (spec schema
// TransactionWriteResult): the row(s) in their final state (both rows of a
// transfer, OUT first) and the post-write balance of every wallet touched.
type WriteResult struct {
	Rows            []TransactionDetail    `json:"rows"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	// The debt person's new balance when the row is a debt entry's wallet
	// side (a personal_debts.DebtPerson; null otherwise).
	PersonBalance any `json:"person_balance"`
	// A date after today: the upcoming item the write made / moved (a
	// recurring RecurringItem; rows = []).
	Upcoming any `json:"upcoming"`
}

// DeleteResult — DELETE /transactions/:id (spec DeleteTransactionResult).
type DeleteResult struct {
	ID              uuid.UUID              `json:"id"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	// The debt entry deleted with it and that person's new balance (nulls
	// when the row was not a debt entry's wallet side).
	DebtEntryID   *uuid.UUID `json:"debt_entry_id"`
	PersonBalance any        `json:"person_balance"`
	// also_delete_link: the recurring item / event bill deleted with it.
	RecurringItemID *uuid.UUID `json:"recurring_item_id"`
	BillID          *uuid.UUID `json:"bill_id"`
}

type CreateRequest struct {
	Type                TxType     `json:"type"                    binding:"required,oneof=expense income transfer"`
	AccountID           *uuid.UUID `json:"account_id"              binding:"omitempty"` // nil = no wallet; transfer still requires account (service validates)
	Amount              float64    `json:"amount"                  binding:"required,gt=0"`
	CategoryID          *uuid.UUID `json:"category_id"             binding:"omitempty"`
	Date                string     `json:"date"                    binding:"required,datetime=2006-01-02"`
	Description         *string    `json:"description"             binding:"omitempty,max=200"`
	Note                *string    `json:"note"                    binding:"omitempty,max=500"`
	TransferToAccountID *uuid.UUID `json:"transfer_to_account_id"  binding:"omitempty"`
	// TagIDs — the caller's tags, attached in the same DB transaction (a
	// transfer's go on its OUT row).
	TagIDs []uuid.UUID `json:"tag_ids" binding:"omitempty"`

	// ProjectID is not accepted (events are joined through the bills
	// endpoints): a non-nil value is rejected with ErrProjectIDNotAllowed.
	// The project resolve flow uses SourceProjectTransactionID and the BE
	// derives the project.
	Splits                     []SplitInput `json:"splits"                         binding:"omitempty,dive"`
	MyShare                    *float64     `json:"my_share"                       binding:"omitempty,gt=0"`
	ProjectID                  *uuid.UUID   `json:"project_id"                     binding:"omitempty"`
	SourceProjectTransactionID *uuid.UUID   `json:"source_project_transaction_id"  binding:"omitempty"`
}

// UpdateRequest — partial. Editable: amount, date, category_id, note,
// account_id (move wallet), transfer_to_account_id (move transfer IN side),
// tag_ids (present = replace the caller's tag set on the row).
// type / transfer_group_id still immutable (delete + recreate).
type UpdateRequest struct {
	Amount              *float64    `json:"amount"                  binding:"omitempty,gt=0"`
	Date                *string     `json:"date"                    binding:"omitempty,datetime=2006-01-02"`
	CategoryID          *uuid.UUID  `json:"category_id"             binding:"omitempty"`
	Description         *string     `json:"description"             binding:"omitempty,max=200"`
	Note                *string     `json:"note"                    binding:"omitempty,max=500"`
	AccountID           *uuid.UUID  `json:"account_id"              binding:"omitempty"`
	TransferToAccountID *uuid.UUID  `json:"transfer_to_account_id"  binding:"omitempty"`
	TagIDs              []uuid.UUID `json:"tag_ids"                 binding:"omitempty"`
	// AlsoUpdateLink — the row is linked (an event bill made from it, a
	// recurring item): change the other side too (the app asks at save).
	AlsoUpdateLink bool `json:"also_update_link"`

	// Track presence so callers can explicitly clear nullable fields.
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
	return nil
}

// CategoryIDChange returns (newID, true) when the request asked to change
// category_id (newID may be nil → clear). (nil, false) means "leave unchanged".
func (r *UpdateRequest) CategoryIDChange() (*uuid.UUID, bool) {
	return r.CategoryID, r.categoryIDPresent || r.CategoryID != nil
}

// DescriptionChange / NoteChange: (value, true) when the body carried the
// key — null or "" clears. A non-nil value set in Go (pending submit)
// also counts, since those callers can't mark presence.
func (r *UpdateRequest) DescriptionChange() (*string, bool) {
	return shared.CleanText(r.Description), r.descriptionPresent || r.Description != nil
}

func (r *UpdateRequest) NoteChange() (*string, bool) {
	return shared.CleanText(r.Note), r.notePresent || r.Note != nil
}

// SetText writes both description and note, nil included (= clear) — for
// callers in Go that mirror another record, e.g. a project row's copy.
func (r *UpdateRequest) SetText(description, note *string) {
	r.Description, r.Note = description, note
	r.descriptionPresent, r.notePresent = true, true
}

// MarkPresent marks body keys as sent (null included) — for Go callers that
// mirror a partial edit of another record (a recurring item's link).
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

// AccountIDChange returns (newID, true) when account_id was present in the
// request body (newID may be nil → clear to floating). (nil, false) = unchanged.
func (r *UpdateRequest) AccountIDChange() (*uuid.UUID, bool) {
	return r.AccountID, r.accountIDPresent || r.AccountID != nil
}

// TransferToAccountIDChange follows the same convention for the IN side of a transfer.
func (r *UpdateRequest) TransferToAccountIDChange() (*uuid.UUID, bool) {
	return r.TransferToAccountID, r.transferToAccountIDPresent
}

// TagIDsChange returns (ids, true) when tag_ids was in the body: the row's
// tag set (the caller's tags) is replaced by ids (null / [] clears). A
// non-nil TagIDs set in Go counts too.
func (r *UpdateRequest) TagIDsChange() ([]uuid.UUID, bool) {
	return r.TagIDs, r.tagIDsPresent || r.TagIDs != nil
}

type ListFilter struct {
	AccountID  *uuid.UUID
	CategoryID *uuid.UUID
	Type       *TxType
	From       *string // YYYY-MM-DD
	To         *string
	NoWallet   bool // true → only rows with account_id IS NULL
	// IncludeArchived — rows on archived wallets are hidden unless asked for
	// (or when listing that wallet itself via AccountID).
	IncludeArchived bool
	// Contract §1 + dashboard drill-down.
	Q               string      // substring over description, note, category (incl. parent) and wallet names
	TagIDs          []uuid.UUID // rows having ANY of these tags
	IncludeChildren bool        // with CategoryID: also its subcategories
	Uncategorized   bool        // only rows with no category
	// CanJoinEvent — only rows the caller may pull into an event (the
	// event's "add existing bills" picker).
	CanJoinEvent bool
	// CounterpartyKey — only the wallet sides of this debt person's entries.
	CounterpartyKey string
	Page            int
	PerPage         int
	Sort            string // date_desc | date_asc | amount_desc | amount_asc
}

// Totals — money over the WHOLE filtered set (every page), same WHERE as
// the list rows. Transfers count in neither side; net = income − expense;
// count = the income + expense rows (pagination.total also counts transfers).
type Totals struct {
	Income  float64 `json:"income"`
	Expense float64 `json:"expense"`
	Net     float64 `json:"net"`
	Count   int     `json:"count"`
}

// ListResponse — spec schema TransactionList.
type ListResponse struct {
	Data       []TransactionDetail `json:"data"`
	Pagination shared.Page         `json:"pagination"`
	Totals     Totals              `json:"totals"`
}

type SummaryRequest struct {
	From       string     // required
	To         string     // required
	AccountID  *uuid.UUID // optional
	CategoryID *uuid.UUID
	Type       *TxType // optional: income | expense — filters rows before grouping
	GroupBy    string  // empty | day | week | month | category | parent_category | account
}

// SummaryGroup — spec schema SummaryGroup. Account is set for
// group_by=account, Category for category / parent_category (null for the
// uncategorized group and for date groups).
type SummaryGroup struct {
	Key      string              `json:"key"`
	Name     string              `json:"name"`
	Account  *shared.AccountRef  `json:"account"`
	Category *shared.CategoryRef `json:"category"`
	Total    float64             `json:"total"`
	Count    int                 `json:"count"`
	// Income / Expense split the group's total by row type (contract §2).
	Income  float64 `json:"income"`
	Expense float64 `json:"expense"`
}

// SummaryResponse — spec schema TransactionSummary (groups is [] without
// group_by).
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
