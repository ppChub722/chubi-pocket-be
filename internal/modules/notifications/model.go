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
	TypeSplitReceived            = "split_received"
	TypeProjectTxRecordedForYou  = "project_tx_recorded_for_you"
	TypeProjectTxChanged         = "project_tx_changed"
	TypeProjectInvite            = "project_invite"
	TypeContactLinkRequest       = "contact_link_request"
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

// Settings is the per-user notification preferences row.
type Settings struct {
	UserID                                       uuid.UUID  `json:"user_id"`
	AutoNotifyLinkedSplitContacts                bool       `json:"auto_notify_linked_split_contacts"`
	AutoAddToPersonalDebtOnSplitNotification     bool       `json:"auto_add_to_personal_debt_on_split_notification"`
	AutoRecordReceivedPayment                    bool       `json:"auto_record_received_payment"`
	AutoResolveOwnInProjects                     bool       `json:"auto_resolve_own_in_projects"`
	DefaultAccountID                             *uuid.UUID `json:"default_account_id"`
	CreatedAt                                    time.Time  `json:"created_at"`
	UpdatedAt                                    time.Time  `json:"updated_at"`
}

type UpdateSettingsRequest struct {
	AutoNotifyLinkedSplitContacts            *bool      `json:"auto_notify_linked_split_contacts"`
	AutoAddToPersonalDebtOnSplitNotification *bool      `json:"auto_add_to_personal_debt_on_split_notification"`
	AutoRecordReceivedPayment                *bool      `json:"auto_record_received_payment"`
	AutoResolveOwnInProjects                 *bool      `json:"auto_resolve_own_in_projects"`
	DefaultAccountID                         *uuid.UUID `json:"default_account_id"`
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
	Note                *string    `json:"note"`
}

type SplitPaidPayload struct {
	SplitID            uuid.UUID  `json:"split_id"`
	PayerUserID        uuid.UUID  `json:"payer_user_id"`
	PayerDisplayName   string     `json:"payer_display_name"`
	Amount             float64    `json:"amount"`
	Currency           string     `json:"currency"`
	PayerTransactionID uuid.UUID  `json:"payer_transaction_id"`
	ProjectID          *uuid.UUID `json:"project_id"`
}

type SplitReceivedPayload struct {
	SplitID               uuid.UUID  `json:"split_id"`
	ReceiverUserID        uuid.UUID  `json:"receiver_user_id"`
	ReceiverDisplayName   string     `json:"receiver_display_name"`
	Amount                float64    `json:"amount"`
	Currency              string     `json:"currency"`
	ReceiverTransactionID uuid.UUID  `json:"receiver_transaction_id"`
	ProjectID             *uuid.UUID `json:"project_id"`
}

type ProjectTxRecordedPayload struct {
	ProjectTransactionID uuid.UUID `json:"project_transaction_id"`
	ProjectID            uuid.UUID `json:"project_id"`
	RecorderUserID       uuid.UUID `json:"recorder_user_id"`
	RecorderDisplayName  string    `json:"recorder_display_name"`
	Amount               float64   `json:"amount"`
	Currency             string    `json:"currency"`
	Type                 string    `json:"type"`
	Note                 *string   `json:"note"`
}

type ProjectTxChangedPayload struct {
	ProjectTransactionID uuid.UUID                  `json:"project_transaction_id"`
	ProjectID            uuid.UUID                  `json:"project_id"`
	EditorUserID         uuid.UUID                  `json:"editor_user_id"`
	EditorDisplayName    string                     `json:"editor_display_name"`
	ChangeKind           string                     `json:"change_kind"` // "edited" | "deleted"
	Diff                 map[string][2]interface{}  `json:"diff,omitempty"`
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
