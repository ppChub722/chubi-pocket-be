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

// Member role enum (account_members.role).
const (
	MemberRoleOwner  = "owner"
	MemberRoleMember = "member"
)

// Report scope enum (account_members.report_scope) — spec §14/5.
const (
	ReportScopeNone = "none"
	ReportScopeOwn  = "own"
	ReportScopeAll  = "all"
)

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
	// Shared-wallet fields (spec §14, pinned contract). `members` holds
	// ACTIVE members only, always including the caller. `is_shared` =
	// active member count > 1. `my_report_scope` is the caller's own
	// membership setting.
	Members       []MemberView `json:"members"`
	MyReportScope string       `json:"my_report_scope"`
	IsShared      bool         `json:"is_shared"`
}

// AccountMember is the account_members DB row. A pending invite has
// JoinedAt == nil; active = JoinedAt set + LeftAt nil.
type AccountMember struct {
	ID          uuid.UUID  `json:"id"`
	AccountID   uuid.UUID  `json:"account_id"`
	UserID      uuid.UUID  `json:"user_id"`
	Role        string     `json:"role"`
	ReportScope string     `json:"report_scope"`
	JoinedAt    *time.Time `json:"joined_at"`
	LeftAt      *time.Time `json:"left_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// MemberView is the pinned members[] entry shape on GET /v1/accounts —
// membership row + denormalized user display fields.
type MemberView struct {
	ID          uuid.UUID        `json:"id"`
	UserID      uuid.UUID        `json:"user_id"`
	DisplayName string           `json:"display_name"`
	IconCode    *shared.IconCode `json:"icon_code"`
	Role        string           `json:"role"`
	JoinedAt    time.Time        `json:"joined_at"`
}

// MemberHistoryView is one row of GET /v1/accounts/:id/members — the full
// membership history for the members screen: active members, pending
// invites, and past members (left_at set). Voided invites (never joined,
// already closed) are excluded by the store query.
type MemberHistoryView struct {
	ID          uuid.UUID        `json:"id"`
	UserID      uuid.UUID        `json:"user_id"`
	DisplayName string           `json:"display_name"`
	IconCode    *shared.IconCode `json:"icon_code"`
	Role        string           `json:"role"`
	Status      string           `json:"status"` // 'pending' | 'active' | 'left'
	JoinedAt    *time.Time       `json:"joined_at"`
	LeftAt      *time.Time       `json:"left_at"`
}

type ListMembersResponse struct {
	Data []MemberHistoryView `json:"data"`
}

type InviteMemberRequest struct {
	Email string `json:"email" binding:"required,email"`
}

type TransferOwnershipRequest struct {
	NewOwnerUserID uuid.UUID `json:"new_owner_user_id" binding:"required"`
}

type ReportScopeRequest struct {
	ReportScope string `json:"report_scope" binding:"required,oneof=none own all"`
}

type ReportScopeResponse struct {
	AccountID   uuid.UUID `json:"account_id"`
	ReportScope string    `json:"report_scope"`
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
