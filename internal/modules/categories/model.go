package categories

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SystemKind names the 6 reserved system categories per spec 05 §2.1.
// The DB column `system_kind` stores these strings; lookups by other modules
// (transfers, opening balance, balance adjustments) go through SystemFor.
type SystemKind string

const (
	SystemOpeningIn  SystemKind = "OPENING_IN"
	SystemOpeningOut SystemKind = "OPENING_OUT"
	SystemAdjustIn   SystemKind = "ADJUST_IN"
	SystemAdjustOut  SystemKind = "ADJUST_OUT"
	SystemTransferIn SystemKind = "TRANSFER_IN"
	SystemTransferOut SystemKind = "TRANSFER_OUT"
)

// Category mirrors the row in `categories`. system_kind is omitted from JSON
// (it's a backend implementation detail; clients see is_system + display name).
type Category struct {
	ID         uuid.UUID  `json:"id"`
	UserID     uuid.UUID  `json:"user_id"`
	Name       string     `json:"name"`
	Type       string     `json:"type"`
	ParentID   *uuid.UUID `json:"parent_id"`
	IsSystem   bool       `json:"is_system"`
	SystemKind *string    `json:"-"`
	Icon       *string    `json:"icon"`
	Color      *string    `json:"color"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
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
	Name     string     `json:"name"      binding:"required,min=1,max=100"`
	Type     string     `json:"type"      binding:"required,oneof=income expense"`
	ParentID *uuid.UUID `json:"parent_id" binding:"omitempty"`
	Icon     *string    `json:"icon"      binding:"omitempty,max=50"`
	Color    *string    `json:"color"     binding:"omitempty,len=7"`
}

// UpdateCategoryRequest — partial update. `type` and `is_system` are
// immutable per spec §3.4; `status` flows through DELETE / restore.
//
// Convention for `parent_id`:
//   - field absent → leave parent unchanged
//   - `"parent_id": null` → set as a root category (clear parent)
//   - `"parent_id": "<uuid>"` → reparent
//
// Custom UnmarshalJSON tracks presence so service layer can distinguish.
type UpdateCategoryRequest struct {
	Name     *string    `json:"name"      binding:"omitempty,min=1,max=100"`
	ParentID *uuid.UUID `json:"parent_id"`
	Icon     *string    `json:"icon"      binding:"omitempty,max=50"`
	Color    *string    `json:"color"     binding:"omitempty,len=7"`

	parentIDPresent bool // true when the "parent_id" JSON key was sent
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
	return nil
}

// ParentIDChange returns (newParent, true) if the request asked to change the
// parent. newParent is nil to mean "make root", otherwise the new parent UUID.
// Returns (nil, false) when the field was absent.
func (r *UpdateCategoryRequest) ParentIDChange() (*uuid.UUID, bool) {
	return r.ParentID, r.parentIDPresent
}

type ListResponse struct {
	Data []Category `json:"data"`
}
