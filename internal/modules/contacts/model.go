package contacts

import (
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const (
	StatusActive   = "active"
	StatusArchived = "archived"
)

type LinkedUser struct {
	ID          uuid.UUID     `json:"id"`
	DisplayName string        `json:"display_name"`
	Username    string        `json:"username"`
	Email       *string       `json:"email"`
	Avatar      shared.Avatar `json:"avatar"`
}

type Contact struct {
	ID           uuid.UUID     `json:"id"`
	UserID       uuid.UUID     `json:"-"`
	Name         string        `json:"name"`
	Nickname     *string       `json:"nickname"`
	ShowNickname bool          `json:"show_nickname"`
	DisplayName  string        `json:"display_name"`
	Email        *string       `json:"email"`
	Phone        *string       `json:"phone"`
	Description  *string       `json:"description"`
	Note         *string       `json:"note"`
	Avatar       shared.Avatar `json:"avatar"` // 0.4.2 1b: linked user's avatar, else the own icon, else initials
	LinkedUserID *uuid.UUID    `json:"-"`
	LinkedUser   *LinkedUser   `json:"linked_user"`
	Status       string        `json:"status"`
	LastUsedAt   *time.Time    `json:"last_used_at"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

type ContactDetail struct {
	Contact
	UnlinkedNames []UnlinkedName `json:"unlinked_names"`
	UnlinkedTotal UnlinkedTotal  `json:"unlinked_total"`
	DeletePreview *DeletePreview `json:"delete_preview"`
}

type CreateContactRequest struct {
	DisplayName  string           `json:"display_name" binding:"required,min=1,max=100"`
	Nickname     *string          `json:"nickname"     binding:"omitempty,max=100"`
	ShowNickname *bool            `json:"show_nickname"`
	Email        *string          `json:"email"        binding:"omitempty,email,max=255"`
	Phone        *string          `json:"phone"        binding:"omitempty,max=50"`
	Description  *string          `json:"description"  binding:"omitempty,max=200"`
	Note         *string          `json:"note"         binding:"omitempty,max=500"`
	IconCode     *shared.IconCode `json:"icon_code"    binding:"omitempty"`
	AbsorbNames  []string         `json:"absorb_names"`
}

type UpdateContactRequest struct {
	DisplayName  *string          `json:"display_name" binding:"omitempty,min=1,max=100"`
	Nickname     *string          `json:"nickname"     binding:"omitempty,max=100"`
	ShowNickname *bool            `json:"show_nickname"`
	Email        *string          `json:"email"        binding:"omitempty,email,max=255"`
	Phone        *string          `json:"phone"        binding:"omitempty,max=50"`
	Description  *string          `json:"description"  binding:"omitempty,max=200"`
	Note         *string          `json:"note"         binding:"omitempty,max=500"`
	IconCode     *shared.IconCode `json:"icon_code"    binding:"omitempty"`

	descriptionPresent bool
	notePresent        bool
	nicknamePresent    bool
}

func (r *UpdateContactRequest) UnmarshalJSON(data []byte) error {
	type alias UpdateContactRequest
	present, err := shared.DecodeTracked(data, (*alias)(r))
	r.descriptionPresent, r.notePresent = present["description"], present["note"]
	r.nicknamePresent = present["nickname"]
	return err
}

func (r *UpdateContactRequest) DescriptionChange() (*string, bool) {
	return shared.TextChange(r.Description, r.descriptionPresent)
}

func (r *UpdateContactRequest) NoteChange() (*string, bool) {
	return shared.TextChange(r.Note, r.notePresent)
}

func (r *UpdateContactRequest) NicknameChange() (*string, bool) {
	return shared.TextChange(r.Nickname, r.nicknamePresent)
}

type CreateContactResponse struct {
	Contact       Contact `json:"contact"`
	AbsorbedCount int     `json:"absorbed_count"`
}

type ListResponse struct {
	Data []Contact `json:"data"`
}

type AbsorbRequest struct {
	Names []string `json:"names" binding:"required,min=1,dive,min=1"`
}

type AbsorbResult struct {
	Contact       Contact `json:"contact"`
	AbsorbedCount int     `json:"absorbed_count"`
}

type DeleteContactResult struct {
	ID             uuid.UUID `json:"id"`
	SplitsRestored int       `json:"splits_restored"`
}

type LinkRequestResult struct {
	Contact      *Contact                   `json:"contact"`
	Notification *shared.NotificationView   `json:"notification"`
	Badges       shared.Badges              `json:"badges"`
	Summary      notifications.InboxSummary `json:"summary"`
}

type UnlinkedNameList struct {
	Data []UnlinkedName `json:"data"`
}

type SenderProfile struct {
	ID          uuid.UUID     `json:"id"`
	DisplayName string        `json:"display_name"`
	Email       *string       `json:"email"`
	Avatar      shared.Avatar `json:"avatar"`
	ContactID   *uuid.UUID    `json:"contact_id"`
}

type LinkExistingContactRequest struct {
	Phone       *string          `json:"phone"     binding:"omitempty,max=50"`
	Description *string          `json:"description" binding:"omitempty,max=200"`
	Note        *string          `json:"note"        binding:"omitempty,max=500"`
	IconCode    *shared.IconCode `json:"icon_code" binding:"omitempty"`
}

type CreateLinkedContactRequest struct {
	Phone       *string          `json:"phone"     binding:"omitempty,max=50"`
	Description *string          `json:"description" binding:"omitempty,max=200"`
	Note        *string          `json:"note"        binding:"omitempty,max=500"`
	IconCode    *shared.IconCode `json:"icon_code" binding:"omitempty"`
}

type UnlinkedTotal struct {
	Names   int `json:"names"`
	Records int `json:"records"`
}

// DeletePreview — with ?include=delete_preview: what a delete would touch.
type DeletePreview struct {
	DebtRecords  int  `json:"debt_records"`  // debts / splits with this person
	EventMembers int  `json:"event_members"` // event members pointing at this contact
	Linked       bool `json:"linked"`        // a linked contact: the link ends too
}
