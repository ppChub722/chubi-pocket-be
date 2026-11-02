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

// Kinds of a person-book row (0.4.1-1 D3).
const (
	KindDebt      = "debt"
	KindRepayment = "repayment"
)

type Entry struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Kind       string // debt | repayment
	Direction  string
	ContactID  *uuid.UUID // counterparty_contact_id
	PersonName string     // counterparty_person_name (typed snapshot)
	// ProjectID — an event lump (a debt booked from an event's summary).
	ProjectID   *uuid.UUID
	Amount      float64
	Currency    string
	Date        string
	Paid        bool
	SourceKind  *string // paper = made from papers (personal_debt_bills)
	Description *string
	Note        *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// CounterpartDebtID — my partner debt in the other person's book (same paper, same pair).
	CounterpartDebtID *uuid.UUID
	DueDate           *string // YYYY-MM-DD; "กำหนดจ่าย" (0.4.0-3)
	// DisconnectedAt (0.4.1-1 D9) — the cross-user pair was disconnected (either side).
	DisconnectedAt *time.Time
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
	// Connected (0.4.1-1 D2) — an existing transaction connected later (can be disconnected).
	Connected bool `json:"connected"`
}

type DebtEntry struct {
	ID          uuid.UUID   `json:"id"`
	Kind        string      `json:"kind"` // debt | repayment (0.4.1-1 D3)
	Key         string      `json:"key"`
	Direction   string      `json:"direction"`
	Amount      float64     `json:"amount"`
	Date        string      `json:"date"`
	Description *string     `json:"description"`
	Note        *string     `json:"note"`
	PrivateNote *string     `json:"private_note"` // 0.4.1-2 S11: mine only, on a debt of a shared bill
	Paid        bool        `json:"paid"`
	DueDate     *string     `json:"due_date"`
	Source      *DebtSource `json:"source"`
	Transaction *EntryTx    `json:"transaction"`
	// PartnerLinked / DisconnectedAt (0.4.1-1 D9) — a copy in another user's book right now; when the
	// pair was disconnected.
	PartnerLinked  bool        `json:"partner_linked"`
	DisconnectedAt *time.Time  `json:"disconnected_at"`
	CreatedAt      time.Time   `json:"created_at"`
	InCurrentCycle *bool       `json:"in_current_cycle"`
	UpdatedAt      time.Time   `json:"updated_at"`
	Origin         *DebtOrigin `json:"origin"`
	// Bills — the papers this debt books (one = a split bill's debt, several = an event lump).
	Bills []DebtBillPart `json:"bills"`
	// InSync — every part holds what its paper says now.
	InSync *bool `json:"in_sync"`
}

// DebtBillPart — one paper's part of a debt, as my book holds it, against the paper now.
type DebtBillPart struct {
	PaperID     uuid.UUID        `json:"split_transaction_id"`
	Event       *shared.EventRef `json:"event"`
	Description *string          `json:"description"`
	Amount      float64          `json:"amount"`
	// Now — what the paper says this pair is now.
	Now    float64 `json:"now"`
	InSync bool    `json:"in_sync"`
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
	// 0.4.0-3 debt pages: the side (receivable | payable | clear), the first line's date
	// ("ติดต่อกันตั้งแต่ …") and the latest line for the people row.
	Side            string        `json:"side"`
	FirstActivityAt *string       `json:"first_activity_at"`
	LastActivity    *LastActivity `json:"last_activity"`
	// NextDueDate — the earliest due date among the unpaid debts on the person's current side
	// (net > 0: paid entries; net < 0: received); null when even or none has one.
	NextDueDate *string `json:"next_due_date"`
}

// LastActivity — a person's latest line (a debt or a repayment), for the people list row.
type LastActivity struct {
	Kind         string    `json:"kind"` // debt | repayment
	ID           uuid.UUID `json:"id"`   // the debt entry id, or the repayment's transaction id
	Title        *string   `json:"title"`
	Direction    string    `json:"direction"` // paid (net +) | received (net −)
	Amount       float64   `json:"amount"`
	SignedAmount float64   `json:"signed_amount"`
	Date         string    `json:"date"`
}

type DebtTotals struct {
	OwedToMe         float64 `json:"owed_to_me"`
	IOwe             float64 `json:"i_owe"`
	Net              float64 `json:"net"`
	OpenCount        int     `json:"open_count"`
	OwedToMePeople   int     `json:"owed_to_me_people"`
	IOwePeople       int     `json:"i_owe_people"`
	Over30DaysPeople int     `json:"over_30_days_people"`
	// The debts list hero (0.4.0-3): the same numbers under the page's names.
	ReceivableTotal float64      `json:"receivable_total"`
	PayableTotal    float64      `json:"payable_total"`
	Counts          PeopleCounts `json:"counts"`
}

// PeopleCounts — the filter chips of the debts list (people shown by default).
type PeopleCounts struct {
	All        int `json:"all"`
	Receivable int `json:"receivable"`
	Payable    int `json:"payable"`
	Overdue30  int `json:"overdue_30"`
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
	// RunningBalance (0.4.0-3) — the person's balance right after this line (book order over all
	// lines: date, created_at, id).
	RunningBalance float64     `json:"running_balance"`
	Origin         *DebtOrigin `json:"origin"` // debts only (the มาจาก card); null on a repayment
}

// StatementDay — one day of the statement page: the balance change of that day's lines and the
// balance at its end (the page's day headers: "เพิ่ม ฿70" / "ลดลง ฿400").
type StatementDay struct {
	Date         string     `json:"date"`
	Change       float64    `json:"change"` // signed: + = they owe me more / I owe less
	BalanceAfter float64    `json:"balance_after"`
	Count        int        `json:"count"`
	lastSeen     *time.Time // the day's latest line so far (created_at)
}

// DebtOrigin — where a debt came from, for the debt detail's "มาจาก" card: paper = made from a
// paper (its first one), cash = a loan / borrow with its own wallet transaction. Null for a
// ledger-only debt.
type DebtOrigin struct {
	Kind          string             `json:"kind"`
	TransactionID *uuid.UUID         `json:"transaction_id"`
	PaperID       *uuid.UUID         `json:"split_transaction_id"`
	EventID       *uuid.UUID         `json:"event_id"`
	Title         *string            `json:"title"`
	Amount        *float64           `json:"amount"`
	Date          *string            `json:"date"`
	Account       *shared.AccountRef `json:"account"`
}

const (
	RepayPay     = "pay"     // I pay them back: an expense (Debt Paid), net +
	RepayReceive = "receive" // they pay me back: an income (Debt Received), net −
)

type RepaymentRequest struct {
	Kind              string      `json:"kind"         binding:"required,oneof=pay receive"`
	Amount            float64     `json:"amount"       binding:"omitempty,gt=0"` // required unless link_transaction_id
	Date              *string     `json:"date"         binding:"omitempty,datetime=2006-01-02"`
	AccountID         *uuid.UUID  `json:"account_id"`
	CreateTransaction *bool       `json:"create_transaction"`
	LinkTransactionID *uuid.UUID  `json:"link_transaction_id"`
	Description       *string     `json:"description"  binding:"omitempty,max=200"`
	Note              *string     `json:"note"         binding:"omitempty,max=500"`
	TagIDs            []uuid.UUID `json:"tag_ids"`
	MarkPaidIDs       []uuid.UUID `json:"mark_paid_ids"`
	// MarkAllPaid (0.4.1-1 D10 "ปิดหนี้") — label every open debt this repayment pays down paid.
	MarkAllPaid bool    `json:"mark_all_paid"`
	PersonName  *string `json:"person_name"  binding:"omitempty,min=1,max=100"`
	// Notify — tell the partner (the other book of a paper debt) that I paid / got paid back.
	Notify bool `json:"notify"`
}

// DebtPreview — POST …/entries or …/repayments with ?preview=true (the debt quick create's result
// box): the person's balance before → after and each touched wallet's, nothing saved. Same shape as
// a transaction PUT preview (people / wallets).
type DebtPreview struct {
	Preview bool                       `json:"preview"`
	People  []transactions.PersonDelta `json:"people"`
	Wallets []transactions.WalletDelta `json:"wallets"`
}

type PersonPage struct {
	DebtPerson
	Counts         EntryCounts     `json:"counts"`
	Items          []StatementItem `json:"items"`
	Days           []StatementDay  `json:"days"`
	SummaryMessage *SummaryMessage `json:"summary_message"`
	Pagination     shared.Page     `json:"pagination"`
	// AutoSyncDebts — my debts auto-sync switch (0.4.1-2 S4), shown on the person page.
	AutoSyncDebts bool `json:"auto_sync_debts"`
	// AutoAcceptDebts — my debts auto-accept switch (0.4.1-2, owner 2026-10-28).
	AutoAcceptDebts bool `json:"auto_accept_debts"`
	// Recurring — the person's "หนี้" recurring settings (0.4.1-2 K13), newest first.
	Recurring []shared.PersonRecurring `json:"recurring"`
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

// LinkTxRequest — POST /personal-debts/{id}/link.
type LinkTxRequest struct {
	TransactionID uuid.UUID `json:"transaction_id" binding:"required"`
}

type WriteResult struct {
	Entry                 DebtEntry                       `json:"entry"`
	PersonBalance         DebtPerson                      `json:"person_balance"`
	PreviousPersonBalance *DebtPerson                     `json:"previous_person_balance"`
	Transaction           *transactions.TransactionDetail `json:"transaction"`
	AccountBalances       []shared.WalletBalance          `json:"account_balances"`
	BalanceChange         *transactions.PersonDelta       `json:"balance_change"` // create only (0.4.0-3); null on other writes
	Item                  *StatementItem                  `json:"item"`           // create only (0.4.1-1)
	Effects               shared.Effects                  `json:"effects"`
}

type DeleteResult struct {
	ID              uuid.UUID              `json:"id"`
	TransactionID   *uuid.UUID             `json:"transaction_id"`
	PersonBalance   DebtPerson             `json:"person_balance"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	Effects         shared.Effects         `json:"effects"`
}

type CreateEntryRequest struct {
	Direction         string      `json:"direction"    binding:"required,oneof=paid received"`
	Amount            float64     `json:"amount"       binding:"omitempty,gt=0"` // required unless link_transaction_id
	Date              *string     `json:"date"         binding:"omitempty,datetime=2006-01-02"`
	AccountID         *uuid.UUID  `json:"account_id"`
	CreateTransaction *bool       `json:"create_transaction"`
	LinkTransactionID *uuid.UUID  `json:"link_transaction_id"` // 0.4.1-1 D10
	Description       *string     `json:"description"  binding:"omitempty,max=200"`
	Note              *string     `json:"note"         binding:"omitempty,max=500"`
	TagIDs            []uuid.UUID `json:"tag_ids"`
	MarkPaidIDs       []uuid.UUID `json:"mark_paid_ids"`
	PersonName        *string     `json:"person_name"  binding:"omitempty,min=1,max=100"`
	DueDate           *string     `json:"due_date"     binding:"omitempty,datetime=2006-01-02"`
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
	// DueDate — null clears it (has("due_date")); absent leaves it.
	DueDate *string `json:"due_date" binding:"omitempty,datetime=2006-01-02"`
	// PrivateNote (0.4.1-2 S11) — only on a debt of a shared bill; null / "" clears.
	PrivateNote *string `json:"private_note"`

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
	Kind      string // debt (default) | repayment | all
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
