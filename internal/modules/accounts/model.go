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
	ID     uuid.UUID `json:"id"`
	UserID uuid.UUID `json:"-"`
	Name   string    `json:"name"`
	Type   string    `json:"type"`
	// Group: asset | liability (shared.AccountGroup), drives the colour group.
	Group          string           `json:"group"`
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
	// Numbers this wallet is known by on bank slips (spec 15 §5). Always an
	// array; edited by the owner.
	Identifiers []Identifier `json:"identifiers"`
	// Shared-wallet fields (spec §14, pinned contract). `members` holds
	// ACTIVE members only, always including the caller. `is_shared` =
	// active member count > 1. `my_report_scope` is the caller's own
	// membership setting.
	Members       []MemberView `json:"members"`
	MyReportScope string       `json:"my_report_scope"`
	IsShared      bool         `json:"is_shared"`
	// MyRole is the caller's role on the wallet; CanEdit = the caller owns it
	// (edits fields, invites, transfers ownership, archives).
	MyRole  string `json:"my_role"`
	CanEdit bool   `json:"can_edit"`
	// TrackBalance — "ติดตามยอดเงิน" (owner 2026-10-12). false: no balance
	// shown, left out of every total / net worth, no adjust; transfers and
	// income / expense reports as usual. Balance is still the running sum.
	TrackBalance bool `json:"track_balance"`
	// Savings — the caller's saving buckets in this wallet: saved, spare,
	// available (= balance − saved), the over-balance warning.
	Savings shared.WalletSavings `json:"savings"`
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
	ID       uuid.UUID        `json:"id"`
	UserID   uuid.UUID        `json:"user_id"`
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
	Role     string           `json:"role"`
	JoinedAt time.Time        `json:"joined_at"`
	IsMe     bool             `json:"is_me"`
}

// MemberHistoryView is one row of GET /v1/accounts/:id/members — the full
// membership history for the members screen: active members, pending
// invites, and past members (left_at set). Voided invites (never joined,
// already closed) are excluded by the store query.
type MemberHistoryView struct {
	ID       uuid.UUID        `json:"id"`
	UserID   uuid.UUID        `json:"user_id"`
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
	Role     string           `json:"role"`
	Status   string           `json:"status"` // pending | active | left
	JoinedAt *time.Time       `json:"joined_at"`
	LeftAt   *time.Time       `json:"left_at"`
	IsMe     bool             `json:"is_me"`
}

// InviteMemberRequest — exactly one of: contact_id (one of my contacts
// linked to an app user; owner 2026-10-11, the app's picker) or email (the
// older form, still accepted).
type InviteMemberRequest struct {
	ContactID *uuid.UUID `json:"contact_id"`
	Email     *string    `json:"email" binding:"omitempty,email"`
}

type TransferOwnershipRequest struct {
	NewOwnerUserID uuid.UUID `json:"new_owner_user_id" binding:"required"`
}

type ReportScopeRequest struct {
	ReportScope string `json:"report_scope" binding:"required,oneof=none own all"`
}

type CreateRequest struct {
	Name           string           `json:"name"             binding:"required,min=1,max=100"`
	Type           string           `json:"type"             binding:"required,oneof=cash bank e_wallet credit_card pay_later"`
	Balance        float64          `json:"balance"`
	IconCode       *shared.IconCode `json:"icon_code"        binding:"omitempty"`
	LogoURL        *string          `json:"logo_url"         binding:"omitempty,url,max=2048"`
	Description    *string          `json:"description"      binding:"omitempty,max=200"`
	Note           *string          `json:"note"             binding:"omitempty,max=500"`
	CreditLimit    *float64         `json:"credit_limit"     binding:"omitempty,gte=0"`
	StatementDate  *int             `json:"statement_date"   binding:"omitempty,min=1,max=31"`
	PaymentDueDate *int             `json:"payment_due_date" binding:"omitempty,min=1,max=31"`
	MinimumPayment *float64         `json:"minimum_payment"  binding:"omitempty,gte=0"`
	Identifiers    []Identifier     `json:"identifiers"`
	// TrackBalance — default true; false = no opening balance.
	TrackBalance *bool `json:"track_balance"`
}

// UpdateRequest — partial. `balance` is intentionally absent (use the
// adjust-balance endpoint per spec §3.4).
//
// `description`, `note`, and `icon_code` are presence-tracked: field absent →
// leave alone; explicit `null` → clear the column.
type UpdateRequest struct {
	Name           *string          `json:"name"             binding:"omitempty,min=1,max=100"`
	Type           *string          `json:"type"             binding:"omitempty,oneof=cash bank e_wallet credit_card pay_later"`
	IconCode       *shared.IconCode `json:"icon_code"`
	LogoURL        *string          `json:"logo_url"         binding:"omitempty,url,max=2048"`
	Description    *string          `json:"description"      binding:"omitempty,max=200"`
	Note           *string          `json:"note"             binding:"omitempty,max=500"`
	CreditLimit    *float64         `json:"credit_limit"     binding:"omitempty,gte=0"`
	StatementDate  *int             `json:"statement_date"   binding:"omitempty,min=1,max=31"`
	PaymentDueDate *int             `json:"payment_due_date" binding:"omitempty,min=1,max=31"`
	MinimumPayment *float64         `json:"minimum_payment"  binding:"omitempty,gte=0"`
	SortOrder      *int             `json:"sort_order"       binding:"omitempty"`
	// Present → replaces the whole list ([] clears it); absent → unchanged.
	Identifiers *[]Identifier `json:"identifiers"`
	// TrackBalance turns "ติดตามยอดเงิน" off / on; CurrentBalance (only with
	// track_balance: true) = the balance it has now (an adjustment row when
	// it differs), so turning it back on is one call.
	TrackBalance   *bool    `json:"track_balance"`
	CurrentBalance *float64 `json:"current_balance"`

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
	return shared.CleanText(r.Description), r.descriptionPresent
}

func (r *UpdateRequest) NoteChange() (*string, bool) {
	return shared.CleanText(r.Note), r.notePresent
}

// NewBalance is a pointer so 0 is a valid target (`required` on a plain
// float64 rejects zero) — and it may be negative (overdraft, card debt).
type AdjustBalanceRequest struct {
	NewBalance  *float64 `json:"new_balance" binding:"required"`
	Date        *string  `json:"date"        binding:"omitempty,datetime=2006-01-02"`
	Description *string  `json:"description" binding:"omitempty,max=200"`
	Note        *string  `json:"note"        binding:"omitempty,max=500"`
}

// AdjustBalanceResult - spec AdjustBalanceResult. Transaction is the
// adjustment row exactly as the transactions module returns it.
type AdjustBalanceResult struct {
	Account         *Account               `json:"account"`
	Transaction     any                    `json:"transaction"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
}

// AccountSummary - spec AccountSummary (income / expense of one wallet over a
// period, default the current month).
type AccountSummary struct {
	From             string  `json:"from"`
	To               string  `json:"to"`
	TotalIncome      float64 `json:"total_income"`
	TotalExpense     float64 `json:"total_expense"`
	Net              float64 `json:"net"`
	TransactionCount int     `json:"transaction_count"`
}

// AccountDetail - spec AccountDetail: the wallet + the optional blocks
// (null when not asked for).
type AccountDetail struct {
	Account
	Summary        *AccountSummary     `json:"summary"`
	MembersHistory []MemberHistoryView `json:"members_history"`
}

// TypeTotal - one by_type row of AccountTotals.
type TypeTotal struct {
	Type    string  `json:"type"`
	Count   int     `json:"count"`
	Balance float64 `json:"balance"`
}

// AccountTotals - spec AccountList.totals.
type AccountTotals struct {
	Assets        float64     `json:"assets"`
	Liabilities   float64     `json:"liabilities"`
	Net           float64     `json:"net"`
	Currency      string      `json:"currency"`
	ByType        []TypeTotal `json:"by_type"`
	CreditUsed    float64     `json:"credit_used"`
	CreditLimit   float64     `json:"credit_limit"`
	SharedCount   int         `json:"shared_count"`
	SharedBalance float64     `json:"shared_balance"`
	// UntrackedCount — wallets with track_balance false (never summed).
	UntrackedCount int `json:"untracked_count"`
}

// AccountList - spec AccountList.
type AccountList struct {
	Data          []Account     `json:"data"`
	Total         int           `json:"total"`
	Totals        AccountTotals `json:"totals"`
	ArchivedCount int           `json:"archived_count"`
}

// MembersResult - spec MembersResult. Account is null when the caller left
// the wallet (removed themselves).
type MembersResult struct {
	Account *Account            `json:"account"`
	Members []MemberHistoryView `json:"members"`
}

// AcceptInviteResult - spec AcceptInviteResult.
type AcceptInviteResult struct {
	Account      *Account                 `json:"account"`
	Notification *shared.NotificationView `json:"notification"`
	Badges       shared.Badges            `json:"badges"`
}

// NotificationActionResult - spec NotificationActionResult.
type NotificationActionResult struct {
	Notification *shared.NotificationView `json:"notification"`
	Badges       shared.Badges            `json:"badges"`
}
