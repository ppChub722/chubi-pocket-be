package accounts

import (
	"encoding/json"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
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
)

func IsCreditType(t string) bool {
	return t == TypeCreditCard || t == TypePayLater
}

const (
	MemberRoleOwner  = "owner"
	MemberRoleMember = "member"
)

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
	Identifiers []Identifier `json:"identifiers"`
	Members       []MemberView `json:"members"`
	MyReportScope string       `json:"my_report_scope"`
	IsShared      bool         `json:"is_shared"`
	MyRole  string `json:"my_role"`
	CanEdit bool   `json:"can_edit"`
	TrackBalance bool `json:"track_balance"`
	Savings shared.WalletSavings `json:"savings"`
	MonthIn      float64 `json:"month_in"`
	MonthOut     float64 `json:"month_out"`
	DueDateNext  *string `json:"due_date_next"`
	DaysUntilDue *int    `json:"days_until_due"`
	CreditUsedPct   *int     `json:"credit_used_pct"`
	CreditAvailable *float64 `json:"credit_available"`
}

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

type MemberView struct {
	ID       uuid.UUID        `json:"id"`
	UserID   uuid.UUID        `json:"user_id"`
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
	Role     string           `json:"role"`
	JoinedAt time.Time        `json:"joined_at"`
	IsMe     bool             `json:"is_me"`
}

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
	TrackBalance *bool `json:"track_balance"`
}

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
	Identifiers *[]Identifier `json:"identifiers"`
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

type AdjustBalanceRequest struct {
	NewBalance  *float64 `json:"new_balance" binding:"required"`
	Date        *string  `json:"date"        binding:"omitempty,datetime=2006-01-02"`
	Description *string  `json:"description" binding:"omitempty,max=200"`
	Note        *string  `json:"note"        binding:"omitempty,max=500"`
}

type AdjustBalanceResult struct {
	Account         *Account               `json:"account"`
	Transaction     any                    `json:"transaction"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
}

type AccountSummary struct {
	From             string  `json:"from"`
	To               string  `json:"to"`
	TotalIncome      float64 `json:"total_income"`
	TotalExpense     float64 `json:"total_expense"`
	Net              float64 `json:"net"`
	TransactionCount int     `json:"transaction_count"`
}

type AccountDetail struct {
	Account
	Summary        *AccountSummary     `json:"summary"`
	MembersHistory []MemberHistoryView `json:"members_history"`
	DeletePreview *DeletePreview `json:"delete_preview"`
}

type TypeTotal struct {
	Type    string  `json:"type"`
	Count   int     `json:"count"`
	Balance float64 `json:"balance"`
}

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

	Total          float64 `json:"total"`
	CashTotal      float64 `json:"cash_total"`
	CardDebt       float64 `json:"card_debt"`
	CardUsedPct    int     `json:"card_used_pct"`
	SavedTotal     float64 `json:"saved_total"`
	AvailableTotal float64 `json:"available_total"`
	CashSplit     []CashShare `json:"cash_split"`
	CardRemaining float64     `json:"card_remaining"`
}

type AccountList struct {
	Data          []Account     `json:"data"`
	Total         int           `json:"total"`
	Totals        AccountTotals `json:"totals"`
	ArchivedCount int           `json:"archived_count"`
}

type MembersResult struct {
	Account *Account            `json:"account"`
	Members []MemberHistoryView `json:"members"`
}

type AcceptInviteResult struct {
	Account      *Account                   `json:"account"`
	Notification *shared.NotificationView   `json:"notification"`
	Badges       shared.Badges              `json:"badges"`
	Summary      notifications.InboxSummary `json:"summary"`
}

type NotificationActionResult struct {
	Notification *shared.NotificationView   `json:"notification"`
	Badges       shared.Badges              `json:"badges"`
	Summary      notifications.InboxSummary `json:"summary"`
}

type CashShare struct {
	Type string `json:"type"`
	Pct  int    `json:"pct"`
}
