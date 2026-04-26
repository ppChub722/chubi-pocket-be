package users

import (
	"time"

	"github.com/google/uuid"
)

// Profile is the response shape for GET /v1/users/me — full identity + preferences.
// Mirrors the response example in design/spec/02-users.md §2.1.
type Profile struct {
	ID              uuid.UUID      `json:"id"`
	Username        string         `json:"username"`
	Email           *string        `json:"email"`
	EmailVerifiedAt *time.Time     `json:"email_verified_at"`
	DisplayName     string         `json:"display_name"`
	Currency        string         `json:"currency"`
	AvatarURL       *string        `json:"avatar_url"`
	Status          string         `json:"status"`
	Preferences     map[string]any `json:"preferences"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type UpdateProfileRequest struct {
	DisplayName *string        `json:"display_name" binding:"omitempty,min=1,max=100"`
	AvatarURL   *string        `json:"avatar_url"   binding:"omitempty,url,max=2048"`
	Currency    *string        `json:"currency"     binding:"omitempty,len=3"`
	Preferences map[string]any `json:"preferences"  binding:"omitempty"`
}

// Allowed preference keys for Phase 0/1. Unknown keys → VALIDATION_ERROR.
var allowedPreferenceKeys = map[string]bool{
	"timezone": true,
	"theme":    true, // accepted from Phase 0 but only user-facing in Phase 2 per spec
	"language": true,
}

func ValidatePreferenceKeys(prefs map[string]any) (string, bool) {
	for k := range prefs {
		if !allowedPreferenceKeys[k] {
			return k, false
		}
	}
	return "", true
}
