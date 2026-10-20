package notifications

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	TypeSplitCreated            = "split_created"
	TypeSplitReceived           = "split_received"
	TypeProjectTxRecordedForYou = "project_tx_recorded_for_you"
	TypeProjectTxChanged        = "project_tx_changed"
	TypeProjectInvite           = "project_invite"
	TypeContactLinkRequest      = "contact_link_request"
	TypeAccountInvite           = "account_invite"
	TypeProjectAdded            = "project_added"
	TypeSplitChanged            = "split_changed"
	TypeRecurringDue            = "recurring_due"
	TypeRecurringEnding         = "recurring_ending"
	TypeRecurringAutoCreated    = "recurring_auto_created"
)

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
	ActorDisplayName *string `json:"actor_display_name,omitempty"`
}

type Settings struct {
	UserID     uuid.UUID `json:"user_id"`
	MutedTypes []string  `json:"muted_types"`
	AutoTypes  []string  `json:"auto_types"`
	DefaultAccountID         *uuid.UUID `json:"default_account_id"`
	AutoResolveOwnInProjects bool       `json:"-"` // read by projects; set via /users/me
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

func (s *Settings) Deliver(notifType string) bool { return !contains(s.MutedTypes, notifType) }

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
	MutedTypes       *[]string  `json:"muted_types"`
	AutoTypes        *[]string  `json:"auto_types"`
	DefaultAccountID *uuid.UUID `json:"default_account_id"`
	ClearDefaultAccount bool `json:"-"`
}

type ListFilter struct {
	Read         string // "true" | "false" | "all"
	Actioned     string
	Dismissed    string      // "" / "false" = visible only (default); "true" = hidden only; "all"
	AwaitsAnswer bool        // only the rows that still wait for an answer
	IDs          []uuid.UUID // set by the service for AwaitsAnswer
	Type         string
	From         *string
	To           *string
	Page         int
	PerPage      int
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
	RecipientDebtID *uuid.UUID `json:"recipient_debt_id"`
	Superseded bool `json:"superseded,omitempty"`
}

type SplitChangedPayload struct {
	SplitID             uuid.UUID `json:"split_id"`
	ParentKind          string    `json:"parent_kind"` // "transaction"
	ParentID            uuid.UUID `json:"parent_id"`
	Change              string    `json:"change"`
	SplitterDisplayName string    `json:"splitter_display_name"`
	OldAmount           float64   `json:"old_amount"`
	NewAmount           float64   `json:"new_amount"`
	Currency            string    `json:"currency"`
	Description         *string   `json:"description"`
	RecipientDebtID uuid.UUID `json:"recipient_debt_id"`
	Superseded      bool      `json:"superseded,omitempty"`
}

const (
	SplitChangeAmount  = "amount"
	SplitChangeRemoved = "removed"
)

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
	PersonalTransactionID *uuid.UUID `json:"personal_transaction_id"`
}

type ProjectTxChangedPayload struct {
	ProjectTransactionID uuid.UUID                 `json:"project_transaction_id"`
	ProjectID            uuid.UUID                 `json:"project_id"`
	EditorUserID         uuid.UUID                 `json:"editor_user_id"`
	EditorDisplayName    string                    `json:"editor_display_name"`
	ChangeKind           string                    `json:"change_kind"` // "edited" | "deleted"
	Diff                 map[string][2]interface{} `json:"diff,omitempty"`
	PersonalTransactionID *uuid.UUID `json:"personal_transaction_id"`
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

type AccountInvitePayload struct {
	AccountMemberID    uuid.UUID `json:"account_member_id"`
	AccountID          uuid.UUID `json:"account_id"`
	AccountName        string    `json:"account_name"`
	InviterUserID      uuid.UUID `json:"inviter_user_id"`
	InviterDisplayName string    `json:"inviter_display_name"`
}

type ProjectAddedPayload struct {
	ProjectMemberID  uuid.UUID `json:"project_member_id"`
	ProjectID        uuid.UUID `json:"project_id"`
	ProjectName      string    `json:"project_name"`
	AdderUserID      uuid.UUID `json:"adder_user_id"`
	AdderDisplayName string    `json:"adder_display_name"`
}

type PersonalUpdate struct {
	Amount      float64 `json:"amount"`
	Date        string  `json:"date"`
	Description *string `json:"description"`
	Note        *string `json:"note"`
}
