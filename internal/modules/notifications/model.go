package notifications

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Notification trigger types. Stored in `notifications.type` and CHECK'd
// at the DB level by migration 000015.
const (
	TypeSplitCreated             = "split_created"
	TypeSplitPaid                = "split_paid"
	TypeSplitReceived            = "split_received" // retired 2026-10-08 — no longer sent
	TypeProjectTxRecordedForYou  = "project_tx_recorded_for_you"
	TypeProjectTxChanged         = "project_tx_changed"
	TypeProjectInvite            = "project_invite"
	TypeContactLinkRequest       = "contact_link_request"
	TypeAccountInvite            = "account_invite"
	TypeProjectAdded             = "project_added"
)

// Notification is the DB row.
type Notification struct {
	ID              uuid.UUID       `json:"id"`
	RecipientUserID uuid.UUID       `json:"recipient_user_id"`
	Type            string          `json:"type"`
	ActorUserID     *uuid.UUID      `json:"actor_user_id"`
	Payload         json.RawMessage `json:"payload"`
	DeepLink        *string         `json:"deep_link"`
	ReadAt          *time.Time      `json:"read_at"`
	ActionedAt      *time.Time      `json:"actioned_at"`
	DismissedAt     *time.Time      `json:"dismissed_at"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	// Hydrated by service for the inbox view.
	ActorDisplayName *string `json:"actor_display_name,omitempty"`
}

// Settings is the per-user notification preferences row (contract §5).
// The recipient decides per notification type: muted (don't deliver —
// and then auto never runs) and auto (run the notification's action as
// soon as it arrives). A muted type keeps its auto choice for when it's
// unmuted.
type Settings struct {
	UserID     uuid.UUID `json:"user_id"`
	MutedTypes []string  `json:"muted_types"`
	AutoTypes  []string  `json:"auto_types"`
	// Wallet the split_paid auto-action records into; nil = no wallet.
	DefaultAccountID *uuid.UUID `json:"default_account_id"`
	// Rows I record myself in a project → my personal copy too. No
	// notification involved (I did it), so it's a plain switch.
	AutoResolveOwnInProjects bool      `json:"auto_resolve_own_in_projects"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
}

// Deliver — the type isn't muted.
func (s *Settings) Deliver(notifType string) bool { return !contains(s.MutedTypes, notifType) }

// Auto — the action runs on arrival: delivered AND switched to auto.
func (s *Settings) Auto(notifType string) bool {
	return s.Deliver(notifType) && contains(s.AutoTypes, notifType)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

type UpdateSettingsRequest struct {
	// Each list replaces the stored one when present; [] clears it.
	MutedTypes               *[]string  `json:"muted_types"`
	AutoTypes                *[]string  `json:"auto_types"`
	DefaultAccountID         *uuid.UUID `json:"default_account_id"`
	AutoResolveOwnInProjects *bool      `json:"auto_resolve_own_in_projects"`
	// Set by the handler on an explicit `"default_account_id": null`.
	ClearDefaultAccount bool `json:"-"`
}

type ListFilter struct {
	Read     string // "true" | "false" | "all"
	Actioned string
	Type     string
	From     *string
	To       *string
	Page     int
	PerPage  int
}

type Pagination struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

type ListResponse struct {
	Data        []Notification `json:"data"`
	UnreadCount int            `json:"unread_count"`
	Pagination  Pagination     `json:"pagination"`
}

// --- Typed payloads (per spec §13.2). dispatcher.go converts these to JSONB. ---

type SplitCreatedPayload struct {
	SplitID             uuid.UUID  `json:"split_id"`
	ParentKind          string     `json:"parent_kind"` // "transaction" | "project_transaction"
	ParentID            uuid.UUID  `json:"parent_id"`
	ProjectID           *uuid.UUID `json:"project_id"`
	SplitterDisplayName string     `json:"splitter_display_name"`
	Amount              float64    `json:"amount"`
	Currency            string     `json:"currency"`
	Description         *string    `json:"description"`
	Note                *string    `json:"note"`
	// The recipient's own debt row (contract §5) — the deep link opens it.
	RecipientDebtID *uuid.UUID `json:"recipient_debt_id"`
}

type SplitPaidPayload struct {
	SplitID            uuid.UUID  `json:"split_id"`
	PayerUserID        uuid.UUID  `json:"payer_user_id"`
	PayerDisplayName   string     `json:"payer_display_name"`
	Amount             float64    `json:"amount"`
	Currency           string     `json:"currency"`
	PayerTransactionID *uuid.UUID `json:"payer_transaction_id"`
	// Set when the recipient'"'"'s auto-action already recorded the receipt.
	RecordedTransactionID *uuid.UUID `json:"recorded_transaction_id"`
	ProjectID          *uuid.UUID `json:"project_id"`
	// The recipient's own debt row (contract §5) — the deep link opens it.
	RecipientDebtID *uuid.UUID `json:"recipient_debt_id"`
}

type ProjectTxRecordedPayload struct {
	ProjectTransactionID uuid.UUID `json:"project_transaction_id"`
	ProjectID            uuid.UUID `json:"project_id"`
	RecorderUserID       uuid.UUID `json:"recorder_user_id"`
	RecorderDisplayName  string    `json:"recorder_display_name"`
	Amount               float64   `json:"amount"`
	Currency             string    `json:"currency"`
	Type                 string    `json:"type"`
	Description          *string   `json:"description"`
	Note                 *string   `json:"note"`
	// The recipient's personal copy (source_project_transaction_id) —
	// created by the auto-action, or the one to update.
	PersonalTransactionID *uuid.UUID `json:"personal_transaction_id"`
}

type ProjectTxChangedPayload struct {
	ProjectTransactionID uuid.UUID                  `json:"project_transaction_id"`
	ProjectID            uuid.UUID                  `json:"project_id"`
	EditorUserID         uuid.UUID                  `json:"editor_user_id"`
	EditorDisplayName    string                     `json:"editor_display_name"`
	ChangeKind           string                     `json:"change_kind"` // "edited" | "deleted"
	Diff                 map[string][2]interface{}  `json:"diff,omitempty"`
	// The recipient's personal copy (source_project_transaction_id) —
	// created by the auto-action, or the one to update.
	PersonalTransactionID *uuid.UUID `json:"personal_transaction_id"`
	// What "update to match" writes to the personal copy (contract §5).
	Suggested *PersonalUpdate `json:"suggested,omitempty"`
}

type ProjectInvitePayload struct {
	ProjectMemberID    uuid.UUID `json:"project_member_id"`
	ProjectID          uuid.UUID `json:"project_id"`
	ProjectName        string    `json:"project_name"`
	InviterUserID      uuid.UUID `json:"inviter_user_id"`
	InviterDisplayName string    `json:"inviter_display_name"`
	Role               string    `json:"role"`
}

type ContactLinkRequestPayload struct {
	ContactID         uuid.UUID `json:"contact_id"`
	SenderUserID      uuid.UUID `json:"sender_user_id"`
	SenderDisplayName string    `json:"sender_display_name"`
}

// AccountInvitePayload — shared-wallet invite (spec §14/4). Accepting
// activates the pending account_members row; rejecting dismisses the
// notification and voids the pending row.
type AccountInvitePayload struct {
	AccountMemberID    uuid.UUID `json:"account_member_id"`
	AccountID          uuid.UUID `json:"account_id"`
	AccountName        string    `json:"account_name"`
	InviterUserID      uuid.UUID `json:"inviter_user_id"`
	InviterDisplayName string    `json:"inviter_display_name"`
}

// ProjectAddedPayload — quick create from bills (spec §10/4.24). Purely
// informational "you've been added to a project" tile: consent is implied
// by the underlying splits / shared wallet, so there is NO accept/reject
// action (leave-project is always available from the project page).
type ProjectAddedPayload struct {
	ProjectMemberID   uuid.UUID `json:"project_member_id"`
	ProjectID         uuid.UUID `json:"project_id"`
	ProjectName       string    `json:"project_name"`
	AdderUserID       uuid.UUID `json:"adder_user_id"`
	AdderDisplayName  string    `json:"adder_display_name"`
}

// PersonalUpdate — new values for a personal copy after its project row
// changed. The amount keeps the copy's basis: a full-amount copy follows
// the new total, a share-only copy follows the new share.
type PersonalUpdate struct {
	Amount float64 `json:"amount"`
	Date   string  `json:"date"`
	Description *string `json:"description"`
	Note   *string `json:"note"`
}
