package contacts

import (
	"time"

	"github.com/google/uuid"
)

const (
	StatusActive   = "active"
	StatusArchived = "archived"
)

type Contact struct {
	ID           uuid.UUID  `json:"id"`
	UserID       uuid.UUID  `json:"user_id"`
	DisplayName  string     `json:"display_name"`
	Nickname     *string    `json:"nickname"`
	Email        *string    `json:"email"`
	Phone        *string    `json:"phone"`
	Notes        *string    `json:"notes"`
	Icon         *string    `json:"icon"`
	LinkedUserID *uuid.UUID `json:"linked_user_id"`
	Status       string     `json:"status"`
	// LastUsedAt is bumped whenever this contact is referenced by a new
	// personal_debts row. Drives the FE typeahead "recent first" sort.
	// Nullable: contacts that haven't been used yet sort last.
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type CreateContactRequest struct {
	DisplayName string  `json:"display_name" binding:"required,min=1,max=100"`
	Nickname    *string `json:"nickname"     binding:"omitempty,min=1,max=100"`
	Email       *string `json:"email"        binding:"omitempty,email,max=255"`
	Phone       *string `json:"phone"        binding:"omitempty,max=50"`
	Notes       *string `json:"notes"`
	Icon        *string `json:"icon"         binding:"omitempty,max=50"`
	// AbsorbNames is accepted in 1b.1 but is a no-op until shared_expense_splits
	// exists (1b.1.b). Wire path is preserved so the wizard UX won't break.
	AbsorbNames []string `json:"absorb_names"`
}

type UpdateContactRequest struct {
	DisplayName *string `json:"display_name" binding:"omitempty,min=1,max=100"`
	Nickname    *string `json:"nickname"     binding:"omitempty,min=1,max=100"`
	Email       *string `json:"email"        binding:"omitempty,email,max=255"`
	Phone       *string `json:"phone"        binding:"omitempty,max=50"`
	Notes       *string `json:"notes"`
	Icon        *string `json:"icon"         binding:"omitempty,max=50"`
}

type CreateContactResponse struct {
	Contact       Contact `json:"contact"`
	AbsorbedCount int     `json:"absorbed_count"`
}

type ListResponse struct {
	Data []Contact `json:"data"`
}
