package user_pack_permissions

import (
	"time"

	"github.com/google/uuid"
)

type UserPackPermission struct {
	ID        uuid.UUID  `json:"id"`
	UserID    uuid.UUID  `json:"user_id"`
	PackID    string     `json:"pack_id"`
	GrantedAt time.Time  `json:"granted_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}
