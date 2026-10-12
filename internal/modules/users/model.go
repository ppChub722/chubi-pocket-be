package users

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Profile is the spec schema Profile (GET /v1/users/me): identity + preferences.
// Mirrors the response example in design/spec/02-users.md §2.1.
type Profile struct {
	ID              uuid.UUID        `json:"id"`
	Username        string           `json:"username"`
	Email           *string          `json:"email"`
	EmailVerifiedAt *time.Time       `json:"email_verified_at"`
	DisplayName     string           `json:"display_name"`
	Currency        string           `json:"currency"`
	IconCode        *shared.IconCode `json:"icon_code"`
	Status          string           `json:"status"`
	Preferences     Preferences      `json:"preferences"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type UpdateProfileRequest struct {
	DisplayName *string          `json:"display_name" binding:"omitempty,min=1,max=100"`
	IconCode    *shared.IconCode `json:"icon_code"    binding:"omitempty"`
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
	// Slip import (spec 15 §7): the expense category fee drafts get — the
	// "ใช้เป็นหมวดค่าธรรมเนียม" switch on a category. UUID string, or null
	// to clear. Checked by Service.UpdateProfile.
	PrefFeeCategoryID: true,
}

// PrefFeeCategoryID — preference key of the fee category.
const PrefFeeCategoryID = "fee_category_id"

func ValidatePreferenceKeys(prefs map[string]any) (string, bool) {
	for k := range prefs {
		if !allowedPreferenceKeys[k] {
			return k, false
		}
	}
	return "", true
}

// Preferences is the spec's known preference keys; every key is always
// present (null when unset).
type Preferences struct {
	Timezone      *string `json:"timezone"`
	Theme         *string `json:"theme"`
	Language      *string `json:"language"`
	FeeCategoryID *string `json:"fee_category_id"`
}

func prefsFromMap(m map[string]any) Preferences {
	str := func(k string) *string {
		if v, ok := m[k].(string); ok && v != "" {
			return &v
		}
		return nil
	}
	return Preferences{
		Timezone: str("timezone"), Theme: str("theme"), Language: str("language"),
		FeeCategoryID: str(PrefFeeCategoryID),
	}
}

// apiStatus maps the stored users.status (active | inactive) to the API enum.
func apiStatus(stored string) string {
	if stored == "active" {
		return "active"
	}
	return "deactivated"
}
