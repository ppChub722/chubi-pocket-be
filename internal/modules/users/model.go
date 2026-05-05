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
	// Email is editable post-registration. When set, it must be a valid
	// address; uniqueness is enforced by the DB index on `users.email` —
	// duplicates surface as ErrEmailExists at the service layer. Pass nil
	// (omit the field) to leave the email unchanged. Clearing back to
	// NULL isn't supported through this endpoint.
	Email       *string        `json:"email"        binding:"omitempty,email,max=255"`
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
