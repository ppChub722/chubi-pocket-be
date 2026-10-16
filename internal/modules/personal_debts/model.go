package personal_debts

// The person ledger (0.3.1-10, spec schemas Debt*). One record type: an
// entry {person, direction paid | received, amount > 0, date, description,
// note, paid label, optional wallet transaction, optional split source}.
// net per person = Σ paid − Σ received (> 0 = they owe me). The "paid" label
// never changes a number; there is no cancel / settled_amount / outstanding:
// stop counting = delete.

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Direction values (my money flow with the person).
const (
	DirectionPaid     = "paid"     // I paid them / for them: net +
	DirectionReceived = "received" // they paid me / for me: net −
)

// Source kinds of a split-made entry (nil = a manual entry).
const (
	SourceTransaction = "transaction" // a split on my bill (source_transaction_id)
	SourceEvent       = "event"       // a split on an event board
	SourcePartner     = "partner"     // another user's split on me (the mirror)
)

// Entry is the personal_debts row.
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
	// The mirror entry in the other user's book (linked splits, §5).
	CounterpartDebtID *uuid.UUID
}

// Key — the entry's person key.
func (e *Entry) Key() string { return shared.DebtKey(e.ContactID, e.PersonName) }

// Signed — the entry's effect on net.
func (e *Entry) Signed() float64 {
	if e.Direction == DirectionPaid {
		return e.Amount
	}
	return -e.Amount
}

// opposite — the other direction (a mirror, a repayment).
func opposite(direction string) string {
	if direction == DirectionPaid {
		return DirectionReceived
	}
	return DirectionPaid
}

// --- responses (spec schemas) ---

// DebtSource — spec schema DebtSource.
type DebtSource struct {
	Kind  string     `json:"kind"`
	ID    *uuid.UUID `json:"id"`
	Title *string    `json:"title"`
}

// EntryTx — spec schema DebtEntryTx.
type EntryTx struct {
	ID      uuid.UUID          `json:"id"`
	Account *shared.AccountRef `json:"account"`
}

// DebtEntry — spec schema DebtEntry.
type DebtEntry struct {
	ID          uuid.UUID   `json:"id"`
	Key         string      `json:"key"`
	Direction   string      `json:"direction"`
	Amount      float64     `json:"amount"`
	Date        string      `json:"date"`
	Description *string     `json:"description"`
	Note        *string     `json:"note"`
	Paid        bool        `json:"paid"`
	Source      *DebtSource `json:"source"`
	Transaction *EntryTx    `json:"transaction"`
	CreatedAt   time.Time   `json:"created_at"`
	// InCurrentCycle — only in the person's entry lists: true = after the last
	// time the balance hit 0; null elsewhere.
	InCurrentCycle *bool     `json:"in_current_cycle"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// DebtPerson — spec schema DebtPerson (a person's balance).
type DebtPerson struct {
	Key            string           `json:"key"`
	Person         shared.PersonRef `json:"person"`
	Net            float64          `json:"net"`
	OpenCount      int              `json:"open_count"`
	EntryCount     int              `json:"entry_count"`
	LastActivityAt *string          `json:"last_activity_at"`
	// ClearedAt — the last time the balance with this person hit 0 (the end of
	// the previous cycle); null when it never did. DaysOutstanding — age in days
	// of the oldest entry of the CURRENT cycle not labelled paid; null when none.
	ClearedAt       *string `json:"cleared_at"`
	DaysOutstanding *int    `json:"days_outstanding"`
}

// DebtTotals — spec schema DebtTotals (the debts header, Home + More tiles).
type DebtTotals struct {
	OwedToMe  float64 `json:"owed_to_me"`
	IOwe      float64 `json:"i_owe"`
	Net       float64 `json:"net"`
	OpenCount int     `json:"open_count"`
	// 0.4.0 summary cards: how many people sit on each side, and how many have
	// had an open entry for more than 30 days (current cycle).
	OwedToMePeople   int `json:"owed_to_me_people"`
	IOwePeople       int `json:"i_owe_people"`
	Over30DaysPeople int `json:"over_30_days_people"`
}

// PeopleList — spec schema DebtPeopleList.
type PeopleList struct {
	Data       []DebtPerson `json:"data"`
	Totals     DebtTotals   `json:"totals"`
	Pagination shared.Page  `json:"pagination"`
}

// EntryCounts — the person page tab counts.
type EntryCounts struct {
	Entries int `json:"entries"`
	Money   int `json:"money"`
	// Current / Earlier — entries of the current cycle / earlier cycles.
	Current int `json:"current"`
	Earlier int `json:"earlier"`
}

// PersonPage — spec schema DebtPersonPage.
type PersonPage struct {
	DebtPerson
	Counts     EntryCounts `json:"counts"`
	Items      []DebtEntry `json:"items"`
	Pagination shared.Page `json:"pagination"`
}

// EntryList — spec schema DebtEntryList.
type EntryList struct {
	Data       []DebtEntry `json:"data"`
	Pagination shared.Page `json:"pagination"`
}

// EntryDetail — spec schema DebtEntryDetail.
type EntryDetail struct {
	Entry         DebtEntry                       `json:"entry"`
	PersonBalance DebtPerson                      `json:"person_balance"`
	Transaction   *transactions.TransactionDetail `json:"transaction"`
}

// WriteResult — spec schema DebtEntryWriteResult.
type WriteResult struct {
	Entry                 DebtEntry                       `json:"entry"`
	PersonBalance         DebtPerson                      `json:"person_balance"`
	PreviousPersonBalance *DebtPerson                     `json:"previous_person_balance"`
	Transaction           *transactions.TransactionDetail `json:"transaction"`
	AccountBalances       []shared.WalletBalance          `json:"account_balances"`
}

// DeleteResult — spec schema DebtEntryDeleteResult.
type DeleteResult struct {
	ID              uuid.UUID              `json:"id"`
	TransactionID   *uuid.UUID             `json:"transaction_id"`
	PersonBalance   DebtPerson             `json:"person_balance"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
}

// SplitChangeResult — spec schema DebtSplitChangeResult.
type SplitChangeResult struct {
	Result        string     `json:"result"` // updated | deleted
	Entry         *DebtEntry `json:"entry"`
	PersonBalance DebtPerson `json:"person_balance"`
}

// --- requests ---

// CreateEntryRequest — spec schema DebtEntryCreateRequest.
type CreateEntryRequest struct {
	Direction   string      `json:"direction"    binding:"required,oneof=paid received"`
	Amount      float64     `json:"amount"       binding:"required,gt=0"`
	Date        *string     `json:"date"         binding:"omitempty,datetime=2006-01-02"`
	AccountID   *uuid.UUID  `json:"account_id"`
	Description *string     `json:"description"  binding:"omitempty,max=200"`
	Note        *string     `json:"note"         binding:"omitempty,max=500"`
	TagIDs      []uuid.UUID `json:"tag_ids"`
	MarkPaidIDs []uuid.UUID `json:"mark_paid_ids"`
	PersonName  *string     `json:"person_name"  binding:"omitempty,min=1,max=100"`
}

// UpdateEntryRequest — spec schema DebtEntryUpdateRequest (partial).
type UpdateEntryRequest struct {
	Direction   *string     `json:"direction"    binding:"omitempty,oneof=paid received"`
	Amount      *float64    `json:"amount"       binding:"omitempty,gt=0"`
	Date        *string     `json:"date"         binding:"omitempty,datetime=2006-01-02"`
	AccountID   *uuid.UUID  `json:"account_id"`
	Description *string     `json:"description"  binding:"omitempty,max=200"`
	Note        *string     `json:"note"         binding:"omitempty,max=500"`
	Paid        *bool       `json:"paid"`
	TagIDs      []uuid.UUID `json:"tag_ids"`
	Key         *string     `json:"key"`
	PersonName  *string     `json:"person_name"  binding:"omitempty,min=1,max=100"`

	present map[string]bool
}

func (r *UpdateEntryRequest) UnmarshalJSON(data []byte) error {
	type alias UpdateEntryRequest
	present, err := shared.DecodeTracked(data, (*alias)(r))
	r.present = present
	return err
}

func (r *UpdateEntryRequest) has(k string) bool { return r.present[k] }

// moneyChange — the request touches something a split-sourced entry can't
// change (only description / note / paid are its own).
func (r *UpdateEntryRequest) moneyChange() bool {
	for _, k := range []string{"direction", "amount", "date", "account_id", "tag_ids", "key", "person_name"} {
		if r.has(k) {
			return true
		}
	}
	return false
}

// PeopleFilter — GET /personal-debts/people.
type PeopleFilter struct {
	Q       string
	All     bool
	Side    string // owed_to_me | i_owe
	Page    int
	PerPage int
}

// EntryFilter — GET /personal-debts/people/:key/entries.
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

// compile-time: the update body is decoded with presence tracking.
var _ json.Unmarshaler = (*UpdateEntryRequest)(nil)
