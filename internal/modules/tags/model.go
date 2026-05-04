package tags

import (
	"time"

	"github.com/google/uuid"
)

type Tag struct {
	ID         uuid.UUID `json:"id"`
	UserID     uuid.UUID `json:"user_id"`
	Name       string    `json:"name"`
	Color      *string   `json:"color"`
	Icon       *string   `json:"icon"`
	UsageCount int       `json:"usage_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type CreateTagRequest struct {
	Name  string  `json:"name"  binding:"required,min=1,max=50"`
	Color *string `json:"color" binding:"omitempty,len=7"`
	Icon  *string `json:"icon"  binding:"omitempty,max=50"`
}

type UpdateTagRequest struct {
	Name  *string `json:"name"  binding:"omitempty,min=1,max=50"`
	Color *string `json:"color" binding:"omitempty,len=7"`
	Icon  *string `json:"icon"  binding:"omitempty,max=50"`
}

type ListResponse struct {
	Data []Tag `json:"data"`
}
