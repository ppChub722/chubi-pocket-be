package tags

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Tag struct {
	ID         uuid.UUID        `json:"id"`
	UserID     uuid.UUID        `json:"user_id"`
	Name       string           `json:"name"`
	IconCode   *shared.IconCode `json:"icon_code"`
	UsageCount int              `json:"usage_count"`
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

type CreateTagRequest struct {
	Name     string           `json:"name"      binding:"required,min=1,max=50"`
	IconCode *shared.IconCode `json:"icon_code" binding:"omitempty"`
}

type UpdateTagRequest struct {
	Name     *string          `json:"name"      binding:"omitempty,min=1,max=50"`
	IconCode *shared.IconCode `json:"icon_code" binding:"omitempty"`
}

type ListResponse struct {
	Data []Tag `json:"data"`
}
