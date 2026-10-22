package personal_debts

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const (
	DirectionPaid     = "paid"     // I paid them / for them: net +
	DirectionReceived = "received" // they paid me / for me: net −
)

const (
	SourceTransaction = "transaction" // a split on my bill (source_transaction_id)
	SourceEvent       = "event"       // a split on an event board
	SourcePartner     = "partner"     // another user's split on me (the mirror)
	SourcePaidBy      = "paid_by"     // someone paid (part of) my expense (paid_for_transaction_id)
)

type Entry struct {
	ID                         uuid.UUID
	UserID                     uuid.UUID
	Direction                  string
	ContactID                  *uuid.UUID // counterparty_contact_id
	PersonName                 string     // counterparty_person_name (typed snapshot)
	SourceTransactionID        *uuid.UUID
	SourceProjectTransactionID *uuid.UUID
	ProjectID                  *uuid.UUID
	Amount                     float64
	Currency                   string
	Date                       string
	Paid                       bool
	SourceKind                 *string
	Description                *string
	Note                       *string
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
	CounterpartDebtID          *uuid.UUID
	// PaidForTransactionID — the expense this debt is the "someone paid for me" part of (0.4.0-3):
	// my own paid_by (source paid_by) or a partner's split mirrored into my book (source partner).
	PaidForTransactionID *uuid.UUID
}

func (e *Entry) Key() string { return shared.DebtKey(e.ContactID, e.PersonName) }

func (e *Entry) Signed() float64 {
	if e.Direction == DirectionPaid {
		return e.Amount
	}
	return -e.Amount
}

func opposite(direction string) string {
	if direction == DirectionPaid {
		return DirectionReceived
	}
	return DirectionPaid
}

type DebtSource struct {
	Kind  string     `json:"kind"`
	ID    *uuid.UUID `json:"id"`
	Title *string    `json:"title"`
}

type EntryTx struct {
	ID      uuid.UUID          `json:"id"`
	Account *shared.AccountRef `json:"account"`
}

type DebtEntry struct {
	ID             uuid.UUID   `json:"id"`
	Key            string      `json:"key"`
	Direction      string      `json:"direction"`
	Amount         float64     `json:"amount"`
	Date           string      `json:"date"`
	Description    *string     `json:"description"`
	Note           *string     `json:"note"`
	Paid           bool        `json:"paid"`
	Source         *DebtSource `json:"source"`
	Transaction    *EntryTx    `json:"transaction"`
	CreatedAt      time.Time   `json:"created_at"`
	InCurrentCycle *bool       `json:"in_current_cycle"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type DebtPerson struct {
	Key            string           `json:"key"`
	Person         shared.PersonRef `json:"person"`
	Net            float64          `json:"net"`
	OpenCount      int              `json:"open_count"`
	EntryCount     int              `json:"entry_count"`
	LastActivityAt *string          `json:"last_activity_at"`
	// ClearedAt — the last time the balance with this person hit 0 (the end of the previous cycle);
	// null when it never did.
	ClearedAt       *string `json:"cleared_at"`
	DaysOutstanding *int    `json:"days_outstanding"`
}

type DebtTotals struct {
	OwedToMe         float64 `json:"owed_to_me"`
	IOwe             float64 `json:"i_owe"`
	Net              float64 `json:"net"`
	OpenCount        int     `json:"open_count"`
	OwedToMePeople   int     `json:"owed_to_me_people"`
	IOwePeople       int     `json:"i_owe_people"`
	Over30DaysPeople int     `json:"over_30_days_people"`
}

type PeopleList struct {
	Data       []DebtPerson `json:"data"`
	Totals     DebtTotals   `json:"totals"`
	Pagination shared.Page  `json:"pagination"`
}

type EntryCounts struct {
	Entries int `json:"entries"`
	Money   int `json:"money"`
	Current int `json:"current"`
	Earlier int `json:"earlier"`
}

const (
	ItemDebt      = "debt"
	ItemRepayment = "repayment"
)

// StatementItem — one line of a person's statement ("ที่มาของยอด"): a debt entry or a repayment
// transaction. Direction follows the sign: paid = net + (a debt I paid for them / a repayment I
// paid back), received = net −.
type StatementItem struct {
	Kind           string      `json:"kind"` // debt | repayment
	ID             uuid.UUID   `json:"id"`   // the debt entry id, or the repayment's transaction id
	Key            string      `json:"key"`
	Direction      string      `json:"direction"`
	Amount         float64     `json:"amount"`
	SignedAmount   float64     `json:"signed_amount"`
	Date           string      `json:"date"`
	Description    *string     `json:"description"`
	Note           *string     `json:"note"`
	Paid           *bool       `json:"paid"`        // the debt's "จ่ายแล้ว" label; null on a repayment
	Source         *DebtSource `json:"source"`      // debts only (split source); null on a repayment
	Transaction    *EntryTx    `json:"transaction"` // a debt's linked tx, or the repayment tx itself
	InCurrentCycle *bool       `json:"in_current_cycle"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

const (
	RepayPay     = "pay"     // I pay them back: an expense (Debt Paid), net +
	RepayReceive = "receive" // they pay me back: an income (Debt Received), net −
)

type RepaymentRequest struct {
	Kind        string      `json:"kind"         binding:"required,oneof=pay receive"`
	Amount      float64     `json:"amount"       binding:"required,gt=0"`
	Date        *string     `json:"date"         binding:"omitempty,datetime=2006-01-02"`
	AccountID   *uuid.UUID  `json:"account_id"`
	Description *string     `json:"description"  binding:"omitempty,max=200"`
	Note        *string     `json:"note"         binding:"omitempty,max=500"`
	TagIDs      []uuid.UUID `json:"tag_ids"`
	MarkPaidIDs []uuid.UUID `json:"mark_paid_ids"`
	PersonName  *string     `json:"person_name"  binding:"omitempty,min=1,max=100"`
}

// RepaymentResult — the repayment transaction (same view as GET /transactions/{id}), its statement
// line, the person's new balance and the touched wallet balances.
type RepaymentResult struct {
	Transaction     *transactions.TransactionDetail `json:"transaction"`
	Item            StatementItem                   `json:"item"`
	PersonBalance   DebtPerson                      `json:"person_balance"`
	AccountBalances []shared.WalletBalance          `json:"account_balances"`
}

type PersonPage struct {
	DebtPerson
	Counts         EntryCounts     `json:"counts"`
	Items          []StatementItem `json:"items"`
	SummaryMessage *SummaryMessage `json:"summary_message"`
	Pagination     shared.Page     `json:"pagination"`
}

type EntryList struct {
	Data       []DebtEntry `json:"data"`
	Pagination shared.Page `json:"pagination"`
}

type EntryDetail struct {
	Entry         DebtEntry                       `json:"entry"`
	PersonBalance DebtPerson                      `json:"person_balance"`
	Transaction   *transactions.TransactionDetail `json:"transaction"`
}

type WriteResult struct {
	Entry                 DebtEntry                       `json:"entry"`
	PersonBalance         DebtPerson                      `json:"person_balance"`
	PreviousPersonBalance *DebtPerson                     `json:"previous_person_balance"`
	Transaction           *transactions.TransactionDetail `json:"transaction"`
	AccountBalances       []shared.WalletBalance          `json:"account_balances"`
}

type DeleteResult struct {
	ID              uuid.UUID              `json:"id"`
	TransactionID   *uuid.UUID             `json:"transaction_id"`
	PersonBalance   DebtPerson             `json:"person_balance"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
}

type SplitChangeResult struct {
	Result        string     `json:"result"` // updated | deleted
	Entry         *DebtEntry `json:"entry"`
	PersonBalance DebtPerson `json:"person_balance"`
}

type CreateEntryRequest struct {
	Direction         string      `json:"direction"    binding:"required,oneof=paid received"`
	Amount            float64     `json:"amount"       binding:"required,gt=0"`
	Date              *string     `json:"date"         binding:"omitempty,datetime=2006-01-02"`
	AccountID         *uuid.UUID  `json:"account_id"`
	CreateTransaction *bool       `json:"create_transaction"`
	Description       *string     `json:"description"  binding:"omitempty,max=200"`
	Note              *string     `json:"note"         binding:"omitempty,max=500"`
	TagIDs            []uuid.UUID `json:"tag_ids"`
	MarkPaidIDs       []uuid.UUID `json:"mark_paid_ids"`
	PersonName        *string     `json:"person_name"  binding:"omitempty,min=1,max=100"`
	// BillID (0.4.0-3) — the event bill this debt settles a share of (the resolve sheet's "as
	// debt"): the entry keeps source event + the bill + its event.
	BillID *uuid.UUID `json:"bill_id"`
}

type UpdateEntryRequest struct {
	Direction         *string     `json:"direction"    binding:"omitempty,oneof=paid received"`
	Amount            *float64    `json:"amount"       binding:"omitempty,gt=0"`
	Date              *string     `json:"date"         binding:"omitempty,datetime=2006-01-02"`
	AccountID         *uuid.UUID  `json:"account_id"`
	CreateTransaction *bool       `json:"create_transaction"`
	Description       *string     `json:"description"  binding:"omitempty,max=200"`
	Note              *string     `json:"note"         binding:"omitempty,max=500"`
	Paid              *bool       `json:"paid"`
	TagIDs            []uuid.UUID `json:"tag_ids"`
	Key               *string     `json:"key"`
	PersonName        *string     `json:"person_name"  binding:"omitempty,min=1,max=100"`

	present map[string]bool
}

func (r *UpdateEntryRequest) UnmarshalJSON(data []byte) error {
	type alias UpdateEntryRequest
	present, err := shared.DecodeTracked(data, (*alias)(r))
	r.present = present
	return err
}

func (r *UpdateEntryRequest) has(k string) bool { return r.present[k] }

// moneyChange — the request touches something a split-sourced entry can't change (only
// description / note / paid are its own).
func (r *UpdateEntryRequest) moneyChange() bool {
	for _, k := range []string{"direction", "amount", "date", "account_id", "create_transaction", "tag_ids", "key", "person_name"} {
		if r.has(k) {
			return true
		}
	}
	return false
}

type PeopleFilter struct {
	Q       string
	All     bool
	Side    string // owed_to_me | i_owe
	Sort    string // oldest (default) | amount | recent
	Page    int
	PerPage int
}

type EntryFilter struct {
	Q         string
	Sort      string
	Direction string
	Paid      *bool
	Money     bool
	Cycle     string // current | earlier | "" = all
	AccountID *uuid.UUID
	From      *string
	To        *string
	Page      int
	PerPage   int
}

var _ json.Unmarshaler = (*UpdateEntryRequest)(nil)
