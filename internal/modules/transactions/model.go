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

// Transaction is the DB row. 1b.1 added `source_split_id`; columns deferred to
// 1b.2 / 1c are `project_id`, `source_project_transaction_id`,
// `scheduled_transaction_id` — see migrations 000006 + 000013 +
// product/phase1b/db.md.
type Transaction struct {
	ID                         uuid.UUID  `json:"id"`
	UserID                     uuid.UUID  `json:"user_id"`
	AccountID                  *uuid.UUID `json:"account_id"` // nil = no wallet (floating)
	Type                       TxType     `json:"type"`
	Amount                     float64    `json:"amount"`
	CategoryID                 *uuid.UUID `json:"category_id"`
	Date                       string     `json:"date"` // YYYY-MM-DD; calendar date in user's tz
	Description                *string    `json:"description"`
	Note                       *string    `json:"note"`
	TransferGroupID            *uuid.UUID `json:"transfer_group_id"`
	SourcePersonalDebtID       *uuid.UUID `json:"source_personal_debt_id"`
	SourceProjectTransactionID *uuid.UUID `json:"source_project_transaction_id"`
	ProjectID                  *uuid.UUID `json:"project_id"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
}

// SplitInput is the per-debtor row inside POST /v1/transactions{splits:[...]}.
// Each entry materializes as one personal_debts row on the splitter's side
// (direction='owed_to_me') + one mirror row on each linked debtor's side
// (direction='i_owe'). See design/spec/12-personal-debts.md.
type SplitInput struct {
	PersonName string     `json:"person_name" binding:"required,min=1,max=100"`
	ContactID  *uuid.UUID `json:"contact_id"  binding:"omitempty"`
	OwedAmount float64    `json:"owed_amount" binding:"required,gt=0"`
}

// SplitEdit — one row of PUT /v1/transactions/:id/splits. With debt_id it
// keeps / re-amounts that existing split; person_name / contact_id on it
// rename or link the person in place, only while the split has no contact
// (left out = unchanged). Without debt_id it adds a person (same fields as
// SplitInput).
type SplitEdit struct {
	DebtID     *uuid.UUID `json:"debt_id"`
	PersonName string     `json:"person_name" binding:"omitempty,max=100"`
	ContactID  *uuid.UUID `json:"contact_id"`
	OwedAmount float64    `json:"owed_amount" binding:"required,gt=0"`
}

// EditSplitsRequest — the WHOLE new list; a split left out is removed, []
// removes them all.
type EditSplitsRequest struct {
	Splits *[]SplitEdit `json:"splits" binding:"required,dive"`
}

// EmbeddedRef is a {id, name} pair used in list/get responses to embed the
// referenced category or account name without forcing the client to JOIN.
type EmbeddedRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// SplitRef — one personal_debts row a split-bill transaction made.
type SplitRef struct {
	DebtID        uuid.UUID  `json:"debt_id"`
	PersonName    string     `json:"person_name"`
	ContactID     *uuid.UUID `json:"contact_id"`
	Direction     string     `json:"direction"`
	Amount        float64    `json:"amount"`
	SettledAmount float64    `json:"settled_amount"`
	Status        string     `json:"status"`
}

// EmbeddedTag enriches a tag ref with icon_code so the FE can render
// chips inline without a second lookup against its own tags cache.
type EmbeddedTag struct {
	ID       uuid.UUID        `json:"id"`
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
}

// AuthorRef is the denormalized row author embedded on shared-wallet
// rows (pinned §14 contract) so the FE renders "who logged it" without a
// second lookup.
type AuthorRef struct {
	UserID      uuid.UUID        `json:"user_id"`
	DisplayName string           `json:"display_name"`
	IconCode    *shared.IconCode `json:"icon_code"`
}

// CategoryRender is the read-only rendering of the AUTHOR's category on
// a shared-wallet row — other members see name + icon but the category
// itself belongs to the author's taxonomy (spec §14/1.2).
type CategoryRender struct {
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
}

// TransactionDetail mirrors `Transaction` plus embedded refs + derived flags.
// Used by GET /v1/transactions and GET /v1/transactions/:id.
type TransactionDetail struct {
	Transaction
	Account      *EmbeddedRef  `json:"account,omitempty"`
	Category     *EmbeddedRef  `json:"category,omitempty"`
	Tags         []EmbeddedTag `json:"tags"`
	HasSplits    bool          `json:"has_splits"`
	SplitCount   int           `json:"split_count"`
	// The author's share (spec 12 §4.5, shared.ShareAmountExpr): amount −
	// what others owe on it (split debts, an event row's member splits).
	// Expense / income only; what reports and budgets count.
	MyShare *float64 `json:"my_share,omitempty"`
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
	// The debts this row made in the caller's book — GET /:id only (nil =
	// key absent on list rows).
	Splits  *[]SplitRef  `json:"splits,omitempty"`
	Project *EmbeddedRef `json:"project,omitempty"`
	IsRecurring  bool          `json:"is_recurring"`
	IsResolve    bool          `json:"is_resolve"`
	// Set on POST response only — not returned in lists.
	AccountBalanceAfter *float64 `json:"account_balance_after,omitempty"`
	// Shared-wallet decorations (pinned §14 contract). Present only on
	// rows whose account has more than one active member.
	CreatedBy       *AuthorRef      `json:"created_by,omitempty"`
	CategoryRender  *CategoryRender `json:"category_render,omitempty"`
	IsLocked        *bool           `json:"is_locked,omitempty"`
	CanEditCategory *bool           `json:"can_edit_category,omitempty"`
}

// TransferResponse is returned by `POST /v1/transactions` (and
// `PUT /v1/transactions/:id` when the targeted row is a transfer).
//
// Both rows are returned in full TransactionDetail shape so the client
// can patch its cache surgically — every transfer mutation touches two
// rows and two account balances; without both rows the destination's
// balance display would lag until the next list refresh.
//
// Spec §3.1: "for transfers, response includes both rows."
type TransferResponse struct {
	TransferGroupID uuid.UUID           `json:"transfer_group_id"`
	Rows            []TransactionDetail `json:"rows"`
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

	// 1b.1 wires Splits + MyShare. ProjectID stays rejected from clients —
	// it's auto-derived from SourceProjectTransactionID when set (project
	// resolve flow). Direct project_id from the body would let any caller
	// link a personal row to any project they don't belong to.
	Splits                     []SplitInput `json:"splits"                         binding:"omitempty,dive"`
	MyShare                    *float64     `json:"my_share"                       binding:"omitempty,gt=0"`
	ProjectID                  *uuid.UUID   `json:"project_id"                     binding:"omitempty"`
	SourceProjectTransactionID *uuid.UUID   `json:"source_project_transaction_id"  binding:"omitempty"`
}

// UpdateRequest — partial. Editable: amount, date, category_id, note,
// account_id (move wallet), transfer_to_account_id (move transfer IN side).
// type / transfer_group_id still immutable (delete + recreate).
type UpdateRequest struct {
	Amount              *float64   `json:"amount"                  binding:"omitempty,gt=0"`
	Date                *string    `json:"date"                    binding:"omitempty,datetime=2006-01-02"`
	CategoryID          *uuid.UUID `json:"category_id"             binding:"omitempty"`
	Description         *string    `json:"description"             binding:"omitempty,max=200"`
	Note                *string    `json:"note"                    binding:"omitempty,max=500"`
	AccountID           *uuid.UUID `json:"account_id"              binding:"omitempty"`
	TransferToAccountID *uuid.UUID `json:"transfer_to_account_id"  binding:"omitempty"`

	// Track presence so callers can explicitly clear nullable fields.
	categoryIDPresent          bool
	descriptionPresent         bool
	notePresent                bool
	accountIDPresent           bool
	transferToAccountIDPresent bool
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
	return nil
}

// CategoryIDChange returns (newID, true) when the request asked to change
// category_id (newID may be nil → clear). (nil, false) means "leave unchanged".
func (r *UpdateRequest) CategoryIDChange() (*uuid.UUID, bool) {
	return r.CategoryID, r.categoryIDPresent
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

// AccountIDChange returns (newID, true) when account_id was present in the
// request body (newID may be nil → clear to floating). (nil, false) = unchanged.
func (r *UpdateRequest) AccountIDChange() (*uuid.UUID, bool) {
	return r.AccountID, r.accountIDPresent
}

// TransferToAccountIDChange follows the same convention for the IN side of a transfer.
func (r *UpdateRequest) TransferToAccountIDChange() (*uuid.UUID, bool) {
	return r.TransferToAccountID, r.transferToAccountIDPresent
}

type ListFilter struct {
	AccountID  *uuid.UUID
	CategoryID *uuid.UUID
	Type       *TxType
	From       *string // YYYY-MM-DD
	To         *string
	NoWallet   bool // true → only rows with account_id IS NULL
	// Contract §1 + dashboard drill-down.
	Q               string      // substring over description, note, category (incl. parent) and wallet names
	TagIDs          []uuid.UUID // rows having ANY of these tags
	IncludeChildren bool        // with CategoryID: also its subcategories
	Uncategorized   bool        // only rows with no category
	Page       int
	PerPage    int
	Sort       string // date_desc | date_asc | amount_desc | amount_asc
}

type Pagination struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
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

type ListResponse struct {
	Data       []TransactionDetail `json:"data"`
	Pagination Pagination          `json:"pagination"`
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

type SummaryGroup struct {
	Key   string  `json:"key"`
	Name  string  `json:"name,omitempty"`
	Total float64 `json:"total"`
	Count int     `json:"count"`
	// Income / Expense split the group's total by row type (contract §2).
	Income  float64 `json:"income"`
	Expense float64 `json:"expense"`
}

type SummaryResponse struct {
	From             string         `json:"from"`
	To               string         `json:"to"`
	Currency         string         `json:"currency"`
	TotalIncome      float64        `json:"total_income"`
	TotalExpense     float64        `json:"total_expense"`
	Net              float64        `json:"net"`
	TransactionCount int            `json:"transaction_count"`
	Groups           []SummaryGroup `json:"groups,omitempty"`
}
