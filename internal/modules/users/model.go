package users

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Profile struct {
	ID              uuid.UUID             `json:"id"`
	Username        string                `json:"username"`
	Email           *string               `json:"email"`
	EmailVerifiedAt *time.Time            `json:"email_verified_at"`
	DisplayName     string                `json:"display_name"`
	Currency        string                `json:"currency"`
	Avatar          shared.Avatar         `json:"avatar"` // 0.4.2 1b (replaces icon_code; my icon = avatar.icon_code)
	AvatarSettings  shared.AvatarSettings `json:"avatar_settings"`
	HasPassword     bool                  `json:"has_password"` // 0.4.2 1b-2: false = made through Telegram, none set yet
	Status          string                `json:"status"`
	Preferences     Preferences           `json:"preferences"`
	DefaultAccount  *shared.AccountRef    `json:"default_account"`
	FeeCategory     *shared.CategoryRef   `json:"fee_category"`
	// AutoSyncEvents / AutoSyncDebts (0.4.1-2 S4) — the two auto-sync switches (user_notification_settings).
	AutoSyncEvents bool `json:"auto_sync_events"`
	AutoSyncDebts  bool `json:"auto_sync_debts"`
	// AutoAcceptEvents / AutoAcceptDebts (0.4.1-2, owner 2026-10-28) — new shared bills land at once.
	AutoAcceptEvents bool `json:"auto_accept_events"`
	AutoAcceptDebts  bool `json:"auto_accept_debts"`
	// The three requests' auto-accept switches (0.4.2-3 F2, default off).
	AutoAcceptEventInvites  bool                `json:"auto_accept_event_invites"`
	AutoAcceptContactLinks  bool                `json:"auto_accept_contact_links"`
	AutoAcceptWalletInvites bool                `json:"auto_accept_wallet_invites"`
	NotificationSummary     NotificationSummary `json:"notification_summary"`
	CreatedAt               time.Time           `json:"created_at"`
	UpdatedAt               time.Time           `json:"updated_at"`
}

type UpdateProfileRequest struct {
	DisplayName             *string                `json:"display_name" binding:"omitempty,min=1,max=100"`
	Currency                *string                `json:"currency" binding:"omitempty,len=3,alpha"`
	IconCode                *shared.IconCode       `json:"icon_code"    binding:"omitempty"`
	AvatarSettings          *shared.AvatarSettings `json:"avatar_settings"`
	Email                   *string                `json:"email"        binding:"omitempty,email,max=255"`
	Preferences             map[string]any         `json:"preferences"  binding:"omitempty"`
	DefaultAccountID        json.RawMessage        `json:"default_account_id"`
	AutoSyncEvents          *bool                  `json:"auto_sync_events"`
	AutoSyncDebts           *bool                  `json:"auto_sync_debts"`
	AutoAcceptEvents        *bool                  `json:"auto_accept_events"`
	AutoAcceptDebts         *bool                  `json:"auto_accept_debts"`
	AutoAcceptEventInvites  *bool                  `json:"auto_accept_event_invites"`
	AutoAcceptContactLinks  *bool                  `json:"auto_accept_contact_links"`
	AutoAcceptWalletInvites *bool                  `json:"auto_accept_wallet_invites"`
}

// Preference keys (0.4.1-2 P1 / P6: theme_mode / theme_id / font_th / font_en / page_prefs added,
// `theme` dropped).
const (
	PrefTimezone   = "timezone"
	PrefThemeMode  = "theme_mode"
	PrefThemeID    = "theme_id"
	PrefLanguage   = "language"
	PrefFontTH     = "font_th"
	PrefFontEN     = "font_en"
	PrefPagePrefs  = "page_prefs"
	maxAppIDLength = 50
	// maxPagePrefBytes — one page's entry in page_prefs, as JSON.
	maxPagePrefBytes = 2048
)

var allowedPreferenceKeys = map[string]bool{
	PrefTimezone: true, PrefThemeMode: true, PrefThemeID: true, PrefLanguage: true,
	PrefFontTH: true, PrefFontEN: true, PrefPagePrefs: true,
	PrefFeeCategoryID: true,
	PrefMoneyStyle:    true,
	PrefMoneyDecimals: true,
}

var (
	ThemeModes = map[string]bool{"light": true, "dark": true, "system": true}
	Languages  = map[string]bool{"th": true, "en": true}
	// PageIDs — the pages whose last choice (sort / filter / ...) page_prefs remembers
	// (0.4.2-1: transactions = the list's sort + filters, never its period; notifications = the
	// inbox filter).
	PageIDs = map[string]bool{
		"wallets": true, "budgets": true, "goals": true, "recurring": true, "events": true,
		"categories": true, "tags": true, "contacts": true, "debts": true, "pending": true,
		"transactions": true, "notifications": true,
	}
)

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
	Timezone      *string        `json:"timezone"`
	ThemeMode     *string        `json:"theme_mode"`
	ThemeID       *string        `json:"theme_id"`
	Language      *string        `json:"language"`
	FontTH        *string        `json:"font_th"`
	FontEN        *string        `json:"font_en"`
	PagePrefs     map[string]any `json:"page_prefs"`
	FeeCategoryID *string        `json:"fee_category_id"`
	MoneyStyle    *string        `json:"money_style"`
	MoneyDecimals *string        `json:"money_decimals"`
}

func prefsFromMap(m map[string]any) Preferences {
	str := func(k string) *string {
		if v, ok := m[k].(string); ok && v != "" {
			return &v
		}
		return nil
	}
	pages := map[string]any{}
	if pp, ok := m[PrefPagePrefs].(map[string]any); ok {
		for k, v := range pp {
			if obj, ok := v.(map[string]any); ok && PageIDs[k] {
				pages[k] = obj
			}
		}
	}
	return Preferences{
		Timezone: str(PrefTimezone), ThemeMode: str(PrefThemeMode), ThemeID: str(PrefThemeID),
		Language: str(PrefLanguage), FontTH: str(PrefFontTH), FontEN: str(PrefFontEN),
		PagePrefs:     pages,
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
	On    int `json:"on"`
	Total int `json:"total"`
}
