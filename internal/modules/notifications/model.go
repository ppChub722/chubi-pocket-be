package notifications

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	TypeProjectInvite        = "project_invite"
	TypeContactLinkRequest   = "contact_link_request"
	TypeAccountInvite        = "account_invite"
	TypeRecurringDue         = "recurring_due"
	TypeRecurringEnding      = "recurring_ending"
	TypeRecurringAutoCreated = "recurring_auto_created"
	TypeDebtDisconnected     = "debt_disconnected" // 0.4.1-1 D9
)

type Notification struct {
	ID               uuid.UUID       `json:"id"`
	RecipientUserID  uuid.UUID       `json:"recipient_user_id"`
	Type             string          `json:"type"`
	ActorUserID      *uuid.UUID      `json:"actor_user_id"`
	Payload          json.RawMessage `json:"payload"`
	DeepLink         *string         `json:"deep_link"`
	ReadAt           *time.Time      `json:"read_at"`
	ActionedAt       *time.Time      `json:"actioned_at"`
	DismissedAt      *time.Time      `json:"dismissed_at"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	ActorDisplayName *string         `json:"actor_display_name,omitempty"`
}

type Settings struct {
	UserID           uuid.UUID  `json:"user_id"`
	MutedTypes       []string   `json:"muted_types"`
	DefaultAccountID *uuid.UUID `json:"default_account_id"`
	// AutoSyncEvents / AutoSyncDebts (0.4.1-2 S4) — the two auto-sync switches: another member's
	// save reaches my book at once (on) or as a notice that asks (off). Independent of mute.
	AutoSyncEvents bool `json:"auto_sync_events"`
	AutoSyncDebts  bool `json:"auto_sync_debts"`
	// AutoAcceptEvents / AutoAcceptDebts (0.4.1-2, owner 2026-10-28) — a NEW shared bill lands in
	// my book at once (on) or as a notice with "เพิ่มในสมุด" (off). Events default on, debts off.
	AutoAcceptEvents bool `json:"auto_accept_events"`
	AutoAcceptDebts  bool `json:"auto_accept_debts"`
	// The three requests' own auto-accept switches (0.4.2-3 F2, migration 125, default off).
	AutoAcceptEventInvites  bool      `json:"auto_accept_event_invites"`
	AutoAcceptContactLinks  bool      `json:"auto_accept_contact_links"`
	AutoAcceptWalletInvites bool `json:"auto_accept_wallet_invites"`
	// OnSplitRemoved — removed from a bill (or leaving it): ask | keep | delete my books of it.
	OnSplitRemoved string `json:"on_split_removed"`
	// AutoAcceptRepayments — a partner's "I paid you back" lands in my book at once.
	AutoAcceptRepayments bool      `json:"auto_accept_repayments"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

const (
	OnRemovedAsk    = "ask"
	OnRemovedKeep   = "keep"
	OnRemovedDelete = "delete"
)

// AcceptRequest — the request type's auto-accept switch (F2); false for every other type.
func (s *Settings) AcceptRequest(notifType string) bool {
	switch notifType {
	case TypeProjectInvite:
		return s.AutoAcceptEventInvites
	case TypeContactLinkRequest:
		return s.AutoAcceptContactLinks
	case TypeAccountInvite:
		return s.AutoAcceptWalletInvites
	}
	return false
}

func (s *Settings) Deliver(notifType string) bool { return !contains(s.MutedTypes, notifType) }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

type UpdateSettingsRequest struct {
	MutedTypes              *[]string  `json:"muted_types"`
	AutoSyncEvents          *bool      `json:"auto_sync_events"`
	AutoSyncDebts           *bool      `json:"auto_sync_debts"`
	AutoAcceptEvents        *bool      `json:"auto_accept_events"`
	AutoAcceptDebts         *bool      `json:"auto_accept_debts"`
	AutoAcceptEventInvites  *bool      `json:"auto_accept_event_invites"`
	AutoAcceptContactLinks  *bool      `json:"auto_accept_contact_links"`
	AutoAcceptWalletInvites *bool      `json:"auto_accept_wallet_invites"`
	AutoAcceptRepayments    *bool      `json:"auto_accept_repayments"`
	OnSplitRemoved          *string    `json:"on_split_removed" binding:"omitempty,oneof=ask keep delete"`
	DefaultAccountID        *uuid.UUID `json:"default_account_id"`
	ClearDefaultAccount     bool       `json:"-"`
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
