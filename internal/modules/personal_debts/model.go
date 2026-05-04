package personal_debts

import (
	"time"

	"github.com/google/uuid"
)

// Direction values. Per-row from the row-owner's perspective.
const (
	DirectionIOwe       = "i_owe"        // counterparty is the creditor
	DirectionOwedToMe   = "owed_to_me"   // counterparty is the debtor
)

// Status values. Direction-agnostic.
const (
	StatusOpen      = "open"
	StatusSettled   = "settled"
	StatusCancelled = "cancelled"
)

// PersonalDebt is the unified bidirectional row.
//
// One row per (user, counterparty, originating event). When a user records
// an expense + splits with someone, the BE creates one row per debtor on
// the splitter's side ('owed_to_me') AND one row on each linked debtor's
// side ('i_owe') — independently mutable books.
//
// `source_transaction_id` points to the originating transaction in this
// row's user_id's book, OR NULL for:
//   - manual debts (cash loans not yet recorded, IOU, broken-item compensation)
//   - debtor-side rows auto-created when the creditor splits with a linked
//     contact (the debtor has no transaction in their book yet).
type PersonalDebt struct {
	ID                          uuid.UUID  `json:"id"`
	UserID                      uuid.UUID  `json:"user_id"`
	Direction                   string     `json:"direction"`
	CounterpartyContactID       *uuid.UUID `json:"counterparty_contact_id"`
	CounterpartyPersonName      string     `json:"counterparty_person_name"`
	SourceTransactionID         *uuid.UUID `json:"source_transaction_id"`
	SourceProjectTransactionID  *uuid.UUID `json:"source_project_transaction_id"`
	ProjectID                   *uuid.UUID `json:"project_id"`
	Amount                      float64    `json:"amount"`
	SettledAmount               float64    `json:"settled_amount"`
	Currency                    string     `json:"currency"`
	Status                      string     `json:"status"`
	Note                        *string    `json:"note"`
	CreatedAt                   time.Time  `json:"created_at"`
	UpdatedAt                   time.Time  `json:"updated_at"`
}

// PersonalDebtView extends the row with computed `outstanding`.
type PersonalDebtView struct {
	PersonalDebt
	Outstanding float64 `json:"outstanding"`
}

// CreateRequest — manual create from the user-facing form. Caller picks
// direction. No source transaction is set (manual debt). For
// transaction-attached debts, the transactions module creates rows
// directly via CreateForTransactionTx, not this endpoint.
type CreateRequest struct {
	Direction              string     `json:"direction"               binding:"required,oneof=i_owe owed_to_me"`
	CounterpartyContactID  *uuid.UUID `json:"counterparty_contact_id" binding:"omitempty"`
	CounterpartyPersonName string     `json:"counterparty_person_name" binding:"required,min=1,max=100"`
	Amount                 float64    `json:"amount"                  binding:"required,gt=0"`
	Currency               string     `json:"currency"                binding:"required,len=3"`
	Note                   *string    `json:"note"`
}

type UpdateRequest struct {
	CounterpartyContactID  *uuid.UUID `json:"counterparty_contact_id" binding:"omitempty"`
	CounterpartyPersonName *string    `json:"counterparty_person_name" binding:"omitempty,min=1,max=100"`
	Amount                 *float64   `json:"amount"                  binding:"omitempty,gt=0"`
	SettledAmount          *float64   `json:"settled_amount"          binding:"omitempty,gte=0"`
	Currency               *string    `json:"currency"                binding:"omitempty,len=3"`
	Status                 *string    `json:"status"                  binding:"omitempty,oneof=open settled cancelled"`
	Note                   *string    `json:"note"`
}

// SettleRequest — record a real money movement against a debt. Creates
// a transaction in the user's book and bumps settled_amount.
//
// For 'i_owe' debts: settling = my expense (paying back).
// For 'owed_to_me' debts: settling = my income (getting paid back).
// Direction is inferred from the debt; caller doesn't pass it.
type SettleRequest struct {
	AccountID uuid.UUID `json:"account_id" binding:"required"`
	Amount    *float64  `json:"amount"     binding:"omitempty,gt=0"`
	Date      *string   `json:"date"       binding:"omitempty,datetime=2006-01-02"`
	Note      *string   `json:"note"`
}

type ListFilter struct {
	Direction              string
	Status                 string
	CounterpartyContactID  *uuid.UUID
	From                   *string
	To                     *string
	Page                   int
	PerPage                int
}

type Pagination struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

type ListResponse struct {
	Data       []PersonalDebtView `json:"data"`
	Pagination Pagination         `json:"pagination"`
}

// PersonRow groups debts by counterparty in the people view.
type PersonRow struct {
	ContactID            *uuid.UUID `json:"contact_id"`
	DisplayName          string     `json:"display_name"`
	OwedToMeOpen         float64    `json:"owed_to_me_open"`
	IOweOpen             float64    `json:"i_owe_open"`
	NetPosition          float64    `json:"net_position"`         // positive = they owe me (net)
	OpenCount            int        `json:"open_count"`
}

type PeopleResponse struct {
	Data            []PersonRow `json:"data"`
	TotalOwedToMe   float64     `json:"total_owed_to_me"`
	TotalIOwe       float64     `json:"total_i_owe"`
	NetPosition     float64     `json:"net_position"`
	Currency        string      `json:"currency"`
}
