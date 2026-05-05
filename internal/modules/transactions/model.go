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
	AccountID                  uuid.UUID  `json:"account_id"`
	Type                       TxType     `json:"type"`
	Amount                     float64    `json:"amount"`
	CategoryID                 *uuid.UUID `json:"category_id"`
	Date                       string     `json:"date"` // YYYY-MM-DD; calendar date in user's tz
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

// EmbeddedRef is a {id, name} pair used in list/get responses to embed the
// referenced category or account name without forcing the client to JOIN.
type EmbeddedRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// EmbeddedTag enriches a tag ref with icon_code so the FE can render
// chips inline without a second lookup against its own tags cache.
type EmbeddedTag struct {
	ID       uuid.UUID        `json:"id"`
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
	IsRecurring  bool          `json:"is_recurring"`
	IsResolve    bool          `json:"is_resolve"`
	// Set on POST response only — not returned in lists.
	AccountBalanceAfter *float64 `json:"account_balance_after,omitempty"`
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
	AccountID           uuid.UUID  `json:"account_id"              binding:"required"`
	Amount              float64    `json:"amount"                  binding:"required,gt=0"`
	CategoryID          *uuid.UUID `json:"category_id"             binding:"omitempty"`
	Date                string     `json:"date"                    binding:"required,datetime=2006-01-02"`
	Note                *string    `json:"note"                    binding:"omitempty"`
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

// UpdateRequest — partial. Editable per spec §3.4: amount, date, category_id,
// note. Splits deferred to 1b. account_id / type / transfer_to_account_id /
// transfer_group_id all immutable (delete + recreate).
type UpdateRequest struct {
	Amount     *float64   `json:"amount"     binding:"omitempty,gt=0"`
	Date       *string    `json:"date"       binding:"omitempty,datetime=2006-01-02"`
	CategoryID *uuid.UUID `json:"category_id" binding:"omitempty"`
	Note       *string    `json:"note"       binding:"omitempty"`

	// Track presence so user can clear category_id by sending null.
	categoryIDPresent bool
	notePresent       bool
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
	_, r.notePresent = probe["note"]
	return nil
}

// CategoryIDChange returns (newID, true) when the request asked to change
// category_id (newID may be nil → clear the category). (nil, false) means
// "leave unchanged".
func (r *UpdateRequest) CategoryIDChange() (*uuid.UUID, bool) {
	return r.CategoryID, r.categoryIDPresent
}

// NoteChange follows the same convention as CategoryIDChange.
func (r *UpdateRequest) NoteChange() (*string, bool) {
	return r.Note, r.notePresent
}

type ListFilter struct {
	AccountID  *uuid.UUID
	CategoryID *uuid.UUID
	Type       *TxType
	From       *string // YYYY-MM-DD
	To         *string
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

type ListResponse struct {
	Data       []TransactionDetail `json:"data"`
	Pagination Pagination          `json:"pagination"`
}

type SummaryRequest struct {
	From       string     // required
	To         string     // required
	AccountID  *uuid.UUID // optional
	CategoryID *uuid.UUID
	GroupBy    string // empty | day | week | month | category | account
}

type SummaryGroup struct {
	Key   string  `json:"key"`
	Name  string  `json:"name,omitempty"`
	Total float64 `json:"total"`
	Count int     `json:"count"`
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
