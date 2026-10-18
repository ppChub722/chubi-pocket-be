package tags

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Tag struct {
	ID          uuid.UUID        `json:"id"`
	UserID      uuid.UUID        `json:"-"`
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

type DeletedResult struct {
	ID uuid.UUID `json:"id"`
}

type ListResponse struct {
	Data []Tag `json:"data"`
}

type BatchRequest struct {
	Create []CreateTagRequest `json:"create" binding:"omitempty,max=200,dive"`
	Update []BatchUpdateItem  `json:"update" binding:"omitempty,max=200,dive"`
	Delete []uuid.UUID        `json:"delete" binding:"omitempty,max=200"`
}

type BatchUpdateItem struct {
	ID  uuid.UUID
	Req UpdateTagRequest
}

func (b *BatchUpdateItem) UnmarshalJSON(data []byte) error {
	var head struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	b.ID = head.ID
	return json.Unmarshal(data, &b.Req)
}

type BatchResult struct {
	Created []Tag       `json:"created"`
	Updated []Tag       `json:"updated"`
	Deleted []uuid.UUID `json:"deleted"`
	Data    []Tag       `json:"data"`
}

type BatchError struct {
	Op    string
	Index int
	Err   error
}

func (e *BatchError) Error() string {
	return e.Op + " #" + strconv.Itoa(e.Index) + ": " + e.Err.Error()
}
func (e *BatchError) Unwrap() error { return e.Err }
