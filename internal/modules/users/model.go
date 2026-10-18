package users

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

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
	DefaultAccount *shared.AccountRef `json:"default_account"`
	AutoCopyOwnEventBills bool `json:"auto_copy_own_event_bills"`
	NotificationSummary NotificationSummary `json:"notification_summary"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

type UpdateProfileRequest struct {
	DisplayName *string `json:"display_name" binding:"omitempty,min=1,max=100"`
	Currency *string          `json:"currency" binding:"omitempty,len=3,alpha"`
	IconCode *shared.IconCode `json:"icon_code"    binding:"omitempty"`
	Email       *string        `json:"email"        binding:"omitempty,email,max=255"`
	Preferences map[string]any `json:"preferences"  binding:"omitempty"`
	DefaultAccountID      json.RawMessage `json:"default_account_id"`
	AutoCopyOwnEventBills *bool           `json:"auto_copy_own_event_bills"`
}

var allowedPreferenceKeys = map[string]bool{
	"timezone": true,
	"theme":    true,
	"language": true,
	PrefFeeCategoryID: true,
	PrefMoneyStyle:    true,
	PrefMoneyDecimals: true,
}

const PrefFeeCategoryID = "fee_category_id"

const PrefMoneyStyle = "money_style"

const PrefMoneyDecimals = "money_decimals"

var MoneyDecimals = map[string]bool{"always": true, "never": true, "auto": true}

var MoneyStyles = map[string]bool{
	"plus_minus": true, "plus_minus_plain": true,
	"parentheses": true, "parentheses_plain": true,
	"color_only": true,
}

func ValidatePreferenceKeys(prefs map[string]any) (string, bool) {
	for k := range prefs {
		if !allowedPreferenceKeys[k] {
			return k, false
		}
	}
	return "", true
}

type Preferences struct {
	Timezone      *string `json:"timezone"`
	Theme         *string `json:"theme"`
	Language      *string `json:"language"`
	FeeCategoryID *string `json:"fee_category_id"`
	MoneyStyle    *string `json:"money_style"`
	MoneyDecimals *string `json:"money_decimals"`
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
		FeeCategoryID: str(PrefFeeCategoryID), MoneyStyle: str(PrefMoneyStyle),
		MoneyDecimals: str(PrefMoneyDecimals),
	}
}

func apiStatus(stored string) string {
	if stored == "active" {
		return "active"
	}
	return "deactivated"
}

type NotificationSummary struct {
	On   int `json:"on"`
	Auto int `json:"auto"`
}
