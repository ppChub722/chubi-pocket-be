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
type Contact struct {
	ID           uuid.UUID        `json:"id"`
	UserID       uuid.UUID        `json:"user_id"`
	DisplayName  string           `json:"display_name"`
	Email        *string          `json:"email"`
	Phone        *string          `json:"phone"`
	Notes        *string          `json:"notes"`
	IconCode     *shared.IconCode `json:"icon_code"`
	LinkedUserID *uuid.UUID       `json:"linked_user_id"`
	Status       string           `json:"status"`
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
