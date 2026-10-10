package tags

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Tag struct {
	ID          uuid.UUID        `json:"id"`
	UserID      uuid.UUID        `json:"user_id"`
	Name        string           `json:"name"`
	Description *string          `json:"description"`
	Note        *string          `json:"note"`
	IconCode    *shared.IconCode `json:"icon_code"`
	UsageCount  int              `json:"usage_count"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type CreateTagRequest struct {
	Name        string           `json:"name"      binding:"required,min=1,max=50"`
	Description *string          `json:"description" binding:"omitempty,max=200"`
	Note        *string          `json:"note"        binding:"omitempty,max=500"`
	IconCode    *shared.IconCode `json:"icon_code" binding:"omitempty"`
}

type UpdateTagRequest struct {
	Name        *string          `json:"name"      binding:"omitempty,min=1,max=50"`
	Description *string          `json:"description" binding:"omitempty,max=200"`
	Note        *string          `json:"note"        binding:"omitempty,max=500"`
	IconCode    *shared.IconCode `json:"icon_code" binding:"omitempty"`

	descriptionPresent bool
	notePresent        bool
}

// UnmarshalJSON records which keys the body carried: an absent description /
// note stays as is; null or "" clears it.
func (r *UpdateTagRequest) UnmarshalJSON(data []byte) error {
	type alias UpdateTagRequest
	present, err := shared.DecodeTracked(data, (*alias)(r))
	r.descriptionPresent, r.notePresent = present["description"], present["note"]
	return err
}

func (r *UpdateTagRequest) DescriptionChange() (*string, bool) {
	return shared.TextChange(r.Description, r.descriptionPresent)
}

func (r *UpdateTagRequest) NoteChange() (*string, bool) {
	return shared.TextChange(r.Note, r.notePresent)
}

type ListResponse struct {
	Data []Tag `json:"data"`
}
