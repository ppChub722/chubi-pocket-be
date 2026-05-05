package categories

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// SystemKind names the 8 reserved system categories. The DB column
// `system_kind` stores these strings; lookups by other modules (transfers,
// opening balance, balance adjustments, debt settlements) go through
// SystemFor.
type SystemKind string

const (
	SystemOpeningIn   SystemKind = "OPENING_IN"
	SystemOpeningOut  SystemKind = "OPENING_OUT"
	SystemAdjustIn    SystemKind = "ADJUST_IN"
	SystemAdjustOut   SystemKind = "ADJUST_OUT"
	SystemTransferIn  SystemKind = "TRANSFER_IN"
	SystemTransferOut SystemKind = "TRANSFER_OUT"
	// Auto-assigned on transactions created by the personal_debts settle
	// flow. Both include_in_report=false so debt repayments don't double-
	// count in spending reports — settled debt outflow/inflow is just
	// returning the underlying amount that was originally accounted for.
	SystemDebtReceived SystemKind = "DEBT_RECEIVED" // income — settled owed_to_me
	SystemDebtPaid     SystemKind = "DEBT_PAID"     // expense — settled i_owe
)

// Category mirrors the row in `categories`. system_kind is omitted from JSON
// (it's a backend implementation detail; clients see is_system + display name).
type Category struct {
	ID              uuid.UUID        `json:"id"`
	UserID          uuid.UUID        `json:"user_id"`
	Name            string           `json:"name"`
	Type            string           `json:"type"`
	ParentID        *uuid.UUID       `json:"parent_id"`
	IsSystem        bool             `json:"is_system"`
	SystemKind      *string          `json:"-"`
	IconCode        *shared.IconCode `json:"icon_code"`
	SortOrder       int              `json:"sort_order"`
	IncludeInReport bool             `json:"include_in_report"`
	Description     *string          `json:"description"`
	Note            *string          `json:"note"`
	Status          string           `json:"status"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

// CategoryDetail is the response shape for GET /v1/categories/:id —
// adds computed fields. Phase 1a `transaction_count` is always 0 until the
// transactions module ships in 1a.3.
type CategoryDetail struct {
	Category
	ParentName       *string `json:"parent_name"`
	Depth            int     `json:"depth"`
	ChildCount       int     `json:"child_count"`
	TransactionCount int     `json:"transaction_count"`
}

type CreateCategoryRequest struct {
	Name            string           `json:"name"              binding:"required,min=1,max=100"`
	Type            string           `json:"type"              binding:"required,oneof=income expense"`
	ParentID        *uuid.UUID       `json:"parent_id"         binding:"omitempty"`
	IconCode        *shared.IconCode `json:"icon_code"         binding:"omitempty"`
	IncludeInReport *bool            `json:"include_in_report" binding:"omitempty"`
	Description     *string          `json:"description"       binding:"omitempty,max=280"`
	Note            *string          `json:"note"              binding:"omitempty,max=280"`
}

// UpdateCategoryRequest — partial update. `type` and `is_system` are
// immutable per spec §3.4; `status` flows through DELETE / restore;
// `sort_order` flows through PATCH /v1/categories/reorder (§3.13).
//
// Presence semantics for `parent_id`, `description`, `note`, `icon_code`:
//   - field absent → leave unchanged
//   - field explicitly `null` → clear (set NULL / make root for parent_id)
//   - field with a value → set
//
// Custom UnmarshalJSON tracks the presence flags so the service layer can
// distinguish "leave alone" from "clear".
type UpdateCategoryRequest struct {
	Name            *string          `json:"name"              binding:"omitempty,min=1,max=100"`
	ParentID        *uuid.UUID       `json:"parent_id"`
	IconCode        *shared.IconCode `json:"icon_code"`
	IncludeInReport *bool            `json:"include_in_report" binding:"omitempty"`
	Description     *string          `json:"description"       binding:"omitempty,max=280"`
	Note            *string          `json:"note"              binding:"omitempty,max=280"`

	parentIDPresent    bool
	iconCodePresent    bool
	descriptionPresent bool
	notePresent        bool
}

func (r *UpdateCategoryRequest) UnmarshalJSON(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	type alias UpdateCategoryRequest
	if err := json.Unmarshal(data, (*alias)(r)); err != nil {
		return err
	}
	_, r.parentIDPresent = probe["parent_id"]
	_, r.iconCodePresent = probe["icon_code"]
	_, r.descriptionPresent = probe["description"]
	_, r.notePresent = probe["note"]
	return nil
}

// ParentIDChange returns (newParent, true) if the request asked to change the
// parent. newParent is nil to mean "make root", otherwise the new parent UUID.
// Returns (nil, false) when the field was absent.
func (r *UpdateCategoryRequest) ParentIDChange() (*uuid.UUID, bool) {
	return r.ParentID, r.parentIDPresent
}

// IconCodeChange returns (iconCode, true) when icon_code was present in the
// request (even if null). Returns (nil, false) when absent (leave unchanged).
func (r *UpdateCategoryRequest) IconCodeChange() (*shared.IconCode, bool) {
	return r.IconCode, r.iconCodePresent
}

// DescriptionChange / NoteChange follow the same convention as
// ParentIDChange — the bool tells the service "the client touched this field".
func (r *UpdateCategoryRequest) DescriptionChange() (*string, bool) {
	return r.Description, r.descriptionPresent
}

func (r *UpdateCategoryRequest) NoteChange() (*string, bool) {
	return r.Note, r.notePresent
}

// ReorderRequest — body of PATCH /v1/categories/reorder. Carries the user's
// post-reorder layout for one (or both) types. See spec §3.13 for semantics.
type ReorderRequest struct {
	Categories []ReorderEntry `json:"categories" binding:"required,min=1,dive"`
}

type ReorderEntry struct {
	ID        uuid.UUID  `json:"id"         binding:"required"`
	ParentID  *uuid.UUID `json:"parent_id"`
	SortOrder int        `json:"sort_order" binding:"min=0"`
}

type ListResponse struct {
	Data []Category `json:"data"`
}
