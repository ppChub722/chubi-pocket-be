package contacts

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const (
	StatusActive   = "active"
	StatusArchived = "archived"
)

// LinkedUser - spec schema LinkedUser: the app user a contact is linked to
// (live profile values).
type LinkedUser struct {
	ID          uuid.UUID        `json:"id"`
	DisplayName string           `json:"display_name"`
	Email       *string          `json:"email"`
	IconCode    *shared.IconCode `json:"icon_code"`
}

// Contact - spec schema Contact. Name is the resolved display name
// (shared.PersonNameSQL: nickname when set and shown, else the linked user's
// live profile name, else display_name). Email / IconCode are resolved too:
// the linked user's when linked, else the contact's own.
type Contact struct {
	ID           uuid.UUID        `json:"id"`
	UserID       uuid.UUID        `json:"-"`
	Name         string           `json:"name"`
	Nickname     *string          `json:"nickname"`
	ShowNickname bool             `json:"show_nickname"`
	DisplayName  string           `json:"display_name"`
	Email        *string          `json:"email"`
	Phone        *string          `json:"phone"`
	Description  *string          `json:"description"`
	Note         *string          `json:"note"`
	IconCode     *shared.IconCode `json:"icon_code"`
	LinkedUserID *uuid.UUID       `json:"-"`
	LinkedUser   *LinkedUser      `json:"linked_user"`
	Status       string           `json:"status"`
	// LastUsedAt is bumped whenever this contact is referenced by a new
	// personal_debts row (drives the "recent first" typeahead sort).
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// ContactDetail - spec schema ContactDetail: the contact plus the
// absorbable free-text names (only with ?include=unlinked_names, else null).
type ContactDetail struct {
	Contact
	UnlinkedNames []UnlinkedName `json:"unlinked_names"`
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
	// AbsorbNames wires existing free-text person_names from the caller's
	// personal_debts to the new contact in the same call. Case-insensitive
	// exact match against counterparty_person_name.
	AbsorbNames []string `json:"absorb_names"`
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

// UnmarshalJSON records which keys the body carried: an absent description /
// note / nickname stays as is; null or "" clears it.
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

// AbsorbRequest - spec AbsorbRequest.
type AbsorbRequest struct {
	Names []string `json:"names" binding:"required,min=1,dive,min=1"`
}

// AbsorbResult - spec AbsorbResult.
type AbsorbResult struct {
	Contact       Contact `json:"contact"`
	AbsorbedCount int     `json:"absorbed_count"`
}

// DeleteContactResult - spec DeleteContactResult.
type DeleteContactResult struct {
	ID             uuid.UUID `json:"id"`
	SplitsRestored int       `json:"splits_restored"`
}

// LinkRequestResult - spec LinkRequestResult.
type LinkRequestResult struct {
	Contact      *Contact                 `json:"contact"`
	Notification *shared.NotificationView `json:"notification"`
	Badges       shared.Badges            `json:"badges"`
}

// UnlinkedNameList - spec UnlinkedNameList.
type UnlinkedNameList struct {
	Data []UnlinkedName `json:"data"`
}

// SenderProfile - spec SenderProfile: the sender of a link request, plus
// the caller's own contact that already links to that sender (null when
// none). Gated to "caller has a contact_link_request notification from this
// user that wasn't rejected", so random users' emails don't leak.
type SenderProfile struct {
	ID          uuid.UUID        `json:"id"`
	DisplayName string           `json:"display_name"`
	Email       *string          `json:"email"`
	IconCode    *shared.IconCode `json:"icon_code"`
	ContactID   *uuid.UUID       `json:"contact_id"`
}

// LinkExistingContactRequest is the optional body of
// `POST /v1/contacts/link-requests/:id/link-existing-contact/:contact_id`.
// All fields optional - additional B-side updates applied at link time.
type LinkExistingContactRequest struct {
	Phone       *string          `json:"phone"     binding:"omitempty,max=50"`
	Description *string          `json:"description" binding:"omitempty,max=200"`
	Note        *string          `json:"note"        binding:"omitempty,max=500"`
	IconCode    *shared.IconCode `json:"icon_code" binding:"omitempty"`
}

// CreateLinkedContactRequest is the optional body of
// `POST /v1/contacts/link-requests/:id/create-linked-contact`. No
// display_name/email - the BE pulls those from the sender's user record.
type CreateLinkedContactRequest struct {
	Phone       *string          `json:"phone"     binding:"omitempty,max=50"`
	Description *string          `json:"description" binding:"omitempty,max=200"`
	Note        *string          `json:"note"        binding:"omitempty,max=500"`
	IconCode    *shared.IconCode `json:"icon_code" binding:"omitempty"`
}
