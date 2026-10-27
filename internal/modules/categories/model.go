package categories

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type SystemKind string

const (
	SystemOpeningIn    SystemKind = "OPENING_IN"
	SystemOpeningOut   SystemKind = "OPENING_OUT"
	SystemAdjustIn     SystemKind = "ADJUST_IN"
	SystemAdjustOut    SystemKind = "ADJUST_OUT"
	SystemTransferIn   SystemKind = "TRANSFER_IN"
	SystemTransferOut  SystemKind = "TRANSFER_OUT"
	SystemDebtReceived SystemKind = "DEBT_RECEIVED" // income — settled owed_to_me
	SystemDebtPaid     SystemKind = "DEBT_PAID"     // expense — settled i_owe
	// 0.4.1-1 D5: the kinds of a DEBT row's transaction (lend / borrow); repayments keep DEBT_*.
	SystemLoanGiven SystemKind = "LOAN_GIVEN" // expense — a debt I paid
	SystemLoanTaken SystemKind = "LOAN_TAKEN" // income — a debt they paid
)

type Category struct {
	ID              uuid.UUID        `json:"id"`
	UserID          uuid.UUID        `json:"-"`
	Name            string           `json:"name"`
	Type            string           `json:"type"`
	ParentID        *uuid.UUID       `json:"parent_id"`
	Depth           int              `json:"depth"`
	Path            []PathItem       `json:"path"`
	IsSystem        bool             `json:"is_system"`
	SystemKind      *string          `json:"system_kind"`
	IconCode        *shared.IconCode `json:"icon_code"`
	SortOrder       int              `json:"sort_order"`
	IncludeInReport bool             `json:"include_in_report"`
	Description     *string          `json:"description"`
	Note            *string          `json:"note"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	MonthSpent      float64          `json:"month_spent"`
	MonthCount      int              `json:"month_count"`
}

type CategoryDetail struct {
	Category
	ParentName       *string `json:"parent_name"`
	ChildCount       int     `json:"child_count"`
	TransactionCount int     `json:"transaction_count"`
	BudgetCount      int     `json:"budget_count"`
	IsFeeCategory    bool    `json:"is_fee_category"`
}

type CreateCategoryRequest struct {
	Name            string           `json:"name"              binding:"required,min=1,max=100"`
	Type            string           `json:"type"              binding:"required,oneof=income expense"`
	ParentID        *uuid.UUID       `json:"parent_id"         binding:"omitempty"`
	IconCode        *shared.IconCode `json:"icon_code"         binding:"omitempty"`
	IncludeInReport *bool            `json:"include_in_report" binding:"omitempty"`
	Description     *string          `json:"description"       binding:"omitempty,max=200"`
	Note            *string          `json:"note"              binding:"omitempty,max=500"`
}

type UpdateCategoryRequest struct {
	Name            *string          `json:"name"              binding:"omitempty,min=1,max=100"`
	ParentID        *uuid.UUID       `json:"parent_id"`
	IconCode        *shared.IconCode `json:"icon_code"`
	IncludeInReport *bool            `json:"include_in_report" binding:"omitempty"`
	Description     *string          `json:"description"       binding:"omitempty,max=200"`
	Note            *string          `json:"note"              binding:"omitempty,max=500"`

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

func (r *UpdateCategoryRequest) ParentIDChange() (*uuid.UUID, bool) {
	return r.ParentID, r.parentIDPresent
}

func (r *UpdateCategoryRequest) IconCodeChange() (*shared.IconCode, bool) {
	return r.IconCode, r.iconCodePresent
}

func (r *UpdateCategoryRequest) DescriptionChange() (*string, bool) {
	return shared.CleanText(r.Description), r.descriptionPresent
}

func (r *UpdateCategoryRequest) NoteChange() (*string, bool) {
	return shared.CleanText(r.Note), r.notePresent
}

type ReorderRequest struct {
	Categories []ReorderEntry `json:"categories" binding:"required,min=1,dive"`
}

type ReorderEntry struct {
	ID        uuid.UUID  `json:"id"         binding:"required"`
	ParentID  *uuid.UUID `json:"parent_id"`
	SortOrder int        `json:"sort_order" binding:"min=0"`
}

type PathItem struct {
	ID       uuid.UUID        `json:"id"`
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
}

type DeleteCategoryResult struct {
	ID         uuid.UUID  `json:"id"`
	Reparented []Category `json:"reparented"`
}

type ListResponse struct {
	Data []Category `json:"data"`
}
