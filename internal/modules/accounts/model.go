package accounts

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const (
	TypeCash       = "cash"
	TypeBank       = "bank"
	TypeEWallet    = "e_wallet"
	TypeCreditCard = "credit_card"
	TypePayLater   = "pay_later"

	StatusActive   = "active"
	StatusArchived = "archived"
	StatusClosed   = "closed"
)

// IsCreditType returns true for the two account types that carry billing fields.
func IsCreditType(t string) bool {
	return t == TypeCreditCard || t == TypePayLater
}

type Account struct {
	ID             uuid.UUID        `json:"id"`
	UserID         uuid.UUID        `json:"user_id"`
	Name           string           `json:"name"`
	Type           string           `json:"type"`
	Balance        float64          `json:"balance"`
	Currency       string           `json:"currency"`
	IconCode       *shared.IconCode `json:"icon_code"`
	LogoURL        *string          `json:"logo_url"`
	Description    *string          `json:"description"`
	Note           *string          `json:"note"`
	Status         string           `json:"status"`
	CreditLimit    *float64         `json:"credit_limit"`
	StatementDate  *int             `json:"statement_date"`
	PaymentDueDate *int             `json:"payment_due_date"`
	MinimumPayment *float64         `json:"minimum_payment"`
	SortOrder      int              `json:"sort_order"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

type CreateRequest struct {
	Name           string           `json:"name"             binding:"required,min=1,max=100"`
	Type           string           `json:"type"             binding:"required,oneof=cash bank e_wallet credit_card pay_later"`
	Balance        float64          `json:"balance"`
	Currency       *string          `json:"currency"         binding:"omitempty,len=3"`
	IconCode       *shared.IconCode `json:"icon_code"        binding:"omitempty"`
	LogoURL        *string          `json:"logo_url"         binding:"omitempty,url,max=2048"`
	Description    *string          `json:"description"      binding:"omitempty,max=280"`
	Note           *string          `json:"note"             binding:"omitempty,max=280"`
	CreditLimit    *float64         `json:"credit_limit"     binding:"omitempty,gte=0"`
	StatementDate  *int             `json:"statement_date"   binding:"omitempty,min=1,max=31"`
	PaymentDueDate *int             `json:"payment_due_date" binding:"omitempty,min=1,max=31"`
	MinimumPayment *float64         `json:"minimum_payment"  binding:"omitempty,gte=0"`
}

// UpdateRequest — partial. `balance` is intentionally absent (use the
// adjust-balance endpoint per spec §3.4).
//
// `description`, `note`, and `icon_code` are presence-tracked: field absent →
// leave alone; explicit `null` → clear the column.
type UpdateRequest struct {
	Name           *string          `json:"name"             binding:"omitempty,min=1,max=100"`
	Type           *string          `json:"type"             binding:"omitempty,oneof=cash bank e_wallet credit_card pay_later"`
	Currency       *string          `json:"currency"         binding:"omitempty,len=3"`
	IconCode       *shared.IconCode `json:"icon_code"`
	LogoURL        *string          `json:"logo_url"         binding:"omitempty,url,max=2048"`
	Description    *string          `json:"description"      binding:"omitempty,max=280"`
	Note           *string          `json:"note"             binding:"omitempty,max=280"`
	Status         *string          `json:"status"           binding:"omitempty,oneof=active archived closed"`
	CreditLimit    *float64         `json:"credit_limit"     binding:"omitempty,gte=0"`
	StatementDate  *int             `json:"statement_date"   binding:"omitempty,min=1,max=31"`
	PaymentDueDate *int             `json:"payment_due_date" binding:"omitempty,min=1,max=31"`
	MinimumPayment *float64         `json:"minimum_payment"  binding:"omitempty,gte=0"`
	SortOrder      *int             `json:"sort_order"       binding:"omitempty"`

	iconCodePresent    bool
	logoURLPresent     bool
	descriptionPresent bool
	notePresent        bool
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
	_, r.iconCodePresent = probe["icon_code"]
	_, r.logoURLPresent = probe["logo_url"]
	_, r.descriptionPresent = probe["description"]
	_, r.notePresent = probe["note"]
	return nil
}

func (r *UpdateRequest) IconCodeChange() (*shared.IconCode, bool) {
	return r.IconCode, r.iconCodePresent
}

func (r *UpdateRequest) LogoURLChange() (*string, bool) {
	return r.LogoURL, r.logoURLPresent
}

func (r *UpdateRequest) DescriptionChange() (*string, bool) {
	return r.Description, r.descriptionPresent
}

func (r *UpdateRequest) NoteChange() (*string, bool) {
	return r.Note, r.notePresent
}

type AdjustBalanceRequest struct {
	NewBalance float64 `json:"new_balance" binding:"required"`
	Date       *string `json:"date"        binding:"omitempty,datetime=2006-01-02"`
	Note       *string `json:"note"        binding:"omitempty"`
}

type AdjustBalanceResponse struct {
	Account                 *Account  `json:"account"`
	AdjustmentTransactionID uuid.UUID `json:"adjustment_transaction_id"`
}

type SummaryResponse struct {
	AccountID        uuid.UUID `json:"account_id"`
	Balance          float64   `json:"balance"`
	Currency         string    `json:"currency"`
	From             string    `json:"from"`
	To               string    `json:"to"`
	TotalIncome      float64   `json:"total_income"`
	TotalExpense     float64   `json:"total_expense"`
	Net              float64   `json:"net"`
	TransactionCount int       `json:"transaction_count"`
}

type ListResponse struct {
	Data  []Account `json:"data"`
	Total int       `json:"total"`
}
