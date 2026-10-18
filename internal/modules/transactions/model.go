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
	TransferGroupID            *uuid.UUID `json:"transfer_group_id"`
	SourcePersonalDebtID       *uuid.UUID `json:"-"` // the debt entry this row is the wallet side of (see Counterparty)
	SourceProjectTransactionID *uuid.UUID `json:"source_project_transaction_id"`
	ProjectID                  *uuid.UUID `json:"-"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
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
	Currency string             `json:"currency"`
	Account  *shared.AccountRef `json:"account"`
	Category     *shared.CategoryRef `json:"category"`
	Tags         []shared.TagRef     `json:"tags"` // the caller's tags on the row
	Project      *shared.EventRef    `json:"project"`
	TransferPeer *TransferPeer       `json:"transfer_peer"` // transfer rows only
	Counterparty *shared.DebtLink `json:"counterparty"`
	EventBill *EventBillLink `json:"event_bill"`
	RecurringItem *shared.RecurringItemLink `json:"recurring_item"`
	MyShare *float64 `json:"my_share"`
	SplitCount int `json:"split_count"`
	Splits *[]SplitRef `json:"splits"`
	CanSplit      bool `json:"can_split"`
	CanEditSplits bool `json:"can_edit_splits"`
	CanJoinEvent  bool `json:"can_join_event"`
	CreatedBy       *shared.UserRef `json:"created_by"`
	IsLocked        bool            `json:"is_locked"`
	CanEditCategory bool            `json:"can_edit_category"`

	HasSplits bool `json:"-"`
}

type EventBillLink struct {
	Event  shared.EventRef `json:"event"`
	BillID uuid.UUID       `json:"bill_id"`
	Linked bool            `json:"linked"`
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
	Upcoming any `json:"upcoming"`
}

type DeleteResult struct {
	ID              uuid.UUID              `json:"id"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	DebtEntryID   *uuid.UUID `json:"debt_entry_id"`
	PersonBalance any        `json:"person_balance"`
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
	TagIDs []uuid.UUID `json:"tag_ids" binding:"omitempty"`

	Splits                     []SplitInput `json:"splits"                         binding:"omitempty,dive"`
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
	AlsoUpdateLink bool `json:"also_update_link"`

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

	AccountIDs  []uuid.UUID // rows on ANY of these wallets
	CategoryIDs []uuid.UUID // rows in ANY of these categories
	Types       []TxType    // rows of ANY of these types
	NoTags      bool        // rows with no tag (OR-ed with TagIDs)
	AmountMin, AmountMax *float64
	TZ                 string
	RangeFrom, RangeTo time.Time
	// IncludeArchived — rows on archived wallets are hidden unless asked for (or when listing that
	// wallet itself via AccountID).
	IncludeArchived bool
	Q               string
	TagIDs          []uuid.UUID // rows having ANY of these tags
	IncludeChildren bool        // with CategoryID: also its subcategories
	Uncategorized   bool        // only rows with no category
	CanJoinEvent bool
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
	Data       []TransactionDetail `json:"data"`
	Pagination shared.Page         `json:"pagination"`
	Totals     Totals              `json:"totals"`
	AvgDailySpend float64      `json:"avg_daily_spend"`
	Daily         []DailyPoint `json:"daily"`
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
	Key      string              `json:"key"`
	Name     string              `json:"name"`
	Account  *shared.AccountRef  `json:"account"`
	Category *shared.CategoryRef `json:"category"`
	Total    float64             `json:"total"`
	Count    int                 `json:"count"`
	Income  float64 `json:"income"`
	Expense float64 `json:"expense"`
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
