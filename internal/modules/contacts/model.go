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

// Contact: nickname column was retired in migration 29. The user's chosen
// label for a person is now display_name only.
//
// For LINKED contacts the FE displays `linked_user_display_name` and
// `linked_user_email` (live-projected from the linked user's row) in
// preference to the contact's own stored values. The contact's stored
// `display_name` / `email` are kept as a snapshot fallback for when
// the contact is later unlinked. Spec §4.4 covers the same pattern for
// `icon_code`.
type Contact struct {
	ID           uuid.UUID        `json:"id"`
	UserID       uuid.UUID        `json:"user_id"`
	DisplayName  string           `json:"display_name"`
	Email        *string          `json:"email"`
	Phone        *string          `json:"phone"`
	Notes        *string          `json:"notes"`
	IconCode     *shared.IconCode `json:"icon_code"`
	LinkedUserID *uuid.UUID       `json:"linked_user_id"`
	// LinkedUserIconCode mirrors the linked user's profile icon at read time.
	// NULL when the contact isn't linked or the linked user has no icon set.
	// Spec §4.4: client renders this in preference to icon_code when present.
	LinkedUserIconCode *shared.IconCode `json:"linked_user_icon_code"`
	// LinkedUserDisplayName / LinkedUserEmail mirror the linked user's
	// display_name / email at read time. NULL when not linked. Same
	// "client renders this in preference to the contact's own value"
	// rule applies — driven by the post-1c policy that linked-contact
	// fields source from the user record.
	LinkedUserDisplayName *string `json:"linked_user_display_name"`
	LinkedUserEmail       *string `json:"linked_user_email"`
	Status                string  `json:"status"`
	// LastUsedAt is bumped whenever this contact is referenced by a new
	// personal_debts row. Drives the FE typeahead "recent first" sort.
	// Nullable: contacts that haven't been used yet sort last.
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type CreateContactRequest struct {
	DisplayName string           `json:"display_name" binding:"required,min=1,max=100"`
	Email       *string          `json:"email"        binding:"omitempty,email,max=255"`
	Phone       *string          `json:"phone"        binding:"omitempty,max=50"`
	Notes       *string          `json:"notes"`
	IconCode    *shared.IconCode `json:"icon_code"    binding:"omitempty"`
	// AbsorbNames wires existing free-text person_names from the caller's
	// personal_debts to the new contact in the same call. Case-insensitive
	// exact match against counterparty_person_name.
	AbsorbNames []string `json:"absorb_names"`
}

type UpdateContactRequest struct {
	DisplayName *string          `json:"display_name" binding:"omitempty,min=1,max=100"`
	Email       *string          `json:"email"        binding:"omitempty,email,max=255"`
	Phone       *string          `json:"phone"        binding:"omitempty,max=50"`
	Notes       *string          `json:"notes"`
	IconCode    *shared.IconCode `json:"icon_code"    binding:"omitempty"`
}

type CreateContactResponse struct {
	Contact       Contact `json:"contact"`
	AbsorbedCount int     `json:"absorbed_count"`
}

type ListResponse struct {
	Data []Contact `json:"data"`
}

// SenderProfile is the slim user shape returned by
// `GET /v1/contacts/link-requests/:id/sender-profile`. The endpoint is
// gated to "caller has a contact_link_request notification from this
// user that hasn't been dismissed/rejected", so we don't leak random
// users' emails. (Allowed even when the request is already actioned —
// the post-accept tap flow needs the sender's profile to render the
// locked display_name/email fields on the create-linked form.)
type SenderProfile struct {
	ID          uuid.UUID        `json:"id"`
	DisplayName string           `json:"display_name"`
	Email       *string          `json:"email"`
	IconCode    *shared.IconCode `json:"icon_code"`
}

// LinkExistingContactRequest is the body of
// `POST /v1/contacts/link-requests/:id/link-existing-contact/:contact_id`.
// All fields optional — they're additional B-side updates the form may
// want to apply at link time. display_name / email are NOT included
// because for linked contacts those are projected from the sender's
// users row at read time.
type LinkExistingContactRequest struct {
	Phone    *string          `json:"phone"     binding:"omitempty,max=50"`
	Notes    *string          `json:"notes"`
	IconCode *shared.IconCode `json:"icon_code" binding:"omitempty"`
}

// CreateLinkedContactRequest is the body of
// `POST /v1/contacts/link-requests/:id/create-linked-contact`. Like
// LinkExistingContactRequest, no display_name/email — the BE pulls
// those from the sender's user record at create time and stores them
// as the contact's snapshot (used as fallback if the contact is later
// unlinked).
type CreateLinkedContactRequest struct {
	Phone    *string          `json:"phone"     binding:"omitempty,max=50"`
	Notes    *string          `json:"notes"`
	IconCode *shared.IconCode `json:"icon_code" binding:"omitempty"`
}
