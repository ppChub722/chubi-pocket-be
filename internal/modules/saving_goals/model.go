package saving_goals

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Status values.
const (
	StatusActive   = "active"
	StatusArchived = "archived"
)

// SavingGoal — stored row, no computed fields.
type SavingGoal struct {
	ID                uuid.UUID        `json:"id"`
	UserID            uuid.UUID        `json:"user_id"`
	Name              string           `json:"name"`
	TargetAmount      float64          `json:"target_amount"`
	LinkedAccountID   uuid.UUID        `json:"linked_account_id"`
	AllocationPct     float64          `json:"allocation_pct"`
	Deadline          *string          `json:"deadline"` // YYYY-MM-DD
	IconCode          *shared.IconCode `json:"icon_code"`
	Status            string           `json:"status"`
	Note              *string          `json:"note"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// LinkedAccountSummary — embedded in SavingGoalView read responses so
// the FE can render the linked account label / balance without a
// second round-trip.
type LinkedAccountSummary struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Balance  float64   `json:"balance"`
	Currency string    `json:"currency"`
}

// SavingGoalView extends SavingGoal with computed progress fields.
// Currency inherits from the linked account; current_amount derives
// from balance × allocation; is_completed is non-sticky (re-flips
// false if balance drops below target).
type SavingGoalView struct {
	SavingGoal
	LinkedAccount   LinkedAccountSummary `json:"linked_account"`
	Currency        string               `json:"currency"`
	CurrentAmount   float64              `json:"current_amount"`
	ProgressPct     float64              `json:"progress_pct"`
	IsCompleted     bool                 `json:"is_completed"`
	RemainingAmount float64              `json:"remaining_amount"`
	DaysRemaining   *int                 `json:"days_remaining"`
}

// CreateRequest — `allocation_pct` is optional. When omitted, the
// service fills it with the linked account's remaining capacity
// (Phase 1c default — never silently mutates existing goals).
type CreateRequest struct {
	Name            string           `json:"name"             binding:"required,min=1,max=100"`
	TargetAmount    float64          `json:"target_amount"    binding:"required,gt=0"`
	LinkedAccountID uuid.UUID        `json:"linked_account_id" binding:"required"`
	AllocationPct   *float64         `json:"allocation_pct"   binding:"omitempty,gt=0,lte=100"`
	Deadline        *string          `json:"deadline"         binding:"omitempty,datetime=2006-01-02"`
	IconCode        *shared.IconCode `json:"icon_code"`
	Note            *string          `json:"note"`
}

// UpdateRequest — partial. linked_account_id is intentionally not
// editable (delete + recreate if wrong). status changes flow through
// the dedicated archive/restore endpoints.
type UpdateRequest struct {
	Name          *string          `json:"name"          binding:"omitempty,min=1,max=100"`
	TargetAmount  *float64         `json:"target_amount" binding:"omitempty,gt=0"`
	AllocationPct *float64         `json:"allocation_pct" binding:"omitempty,gt=0,lte=100"`
	Deadline      *string          `json:"deadline"      binding:"omitempty,datetime=2006-01-02"`
	IconCode      *shared.IconCode `json:"icon_code"`
	Note          *string          `json:"note"`
}

type ListFilter struct {
	Status      string // 'active' (default), 'archived', 'all'
	AccountID   *uuid.UUID
	IsCompleted *bool
}

type ListResponse struct {
	Data []SavingGoalView `json:"data"`
}

// AccountAllocationsResponse — backs `GET /accounts/:id/saving-allocations`.
// Used by the FE to render the per-account allocation pie.
type AccountAllocationGoal struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	AllocationPct float64   `json:"allocation_pct"`
	CurrentAmount float64   `json:"current_amount"`
}

type AccountAllocationsResponse struct {
	AccountID         uuid.UUID               `json:"account_id"`
	AccountBalance    float64                 `json:"account_balance"`
	Currency          string                  `json:"currency"`
	ActiveGoals       []AccountAllocationGoal `json:"active_goals"`
	AllocatedPct      float64                 `json:"allocated_pct"`
	UnallocatedPct    float64                 `json:"unallocated_pct"`
	UnallocatedAmount float64                 `json:"unallocated_amount"`
}
