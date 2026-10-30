package users

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Service struct {
	store            *Store
	feeCategoryCheck func(ctx context.Context, userID, categoryID uuid.UUID) error
}

func (s *Service) WithFeeCategoryCheck(fn func(ctx context.Context, userID, categoryID uuid.UUID) error) {
	s.feeCategoryCheck = fn
}

var ErrInvalidFeeCategory = errors.New("fee_category_id must be one of your expense categories")

// ValidTimezone: an IANA name Go's embedded tz database knows. SQL uses the
// stored name as is (shared.TodaySQL), so only real zones may be saved;
// "" and "Local" are not zone names.
func ValidTimezone(name string) bool {
	if name == "" || name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

func (s *Service) FeeCategoryID(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error) {
	prof, err := s.store.GetProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	str := ""
	if prof.Preferences.FeeCategoryID != nil {
		str = *prof.Preferences.FeeCategoryID
	}
	if str == "" {
		return nil, nil
	}
	id, err := uuid.Parse(str)
	if err != nil {
		return nil, nil
	}
	return &id, nil
}

func NewService(s *Store) *Service {
	return &Service{store: s}
}

var ErrUnknownPreferenceKey = errors.New("unknown preference key")

func (s *Service) GetProfile(ctx context.Context, id uuid.UUID) (*Profile, error) {
	return s.store.GetProfile(ctx, id)
}

func (s *Service) UpdateProfile(ctx context.Context, id uuid.UUID, req UpdateProfileRequest) (*Profile, error) {
	if len(req.Preferences) > 0 {
		if k, ok := ValidatePreferenceKeys(req.Preferences); !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownPreferenceKey, k)
		}
		for k, v := range req.Preferences {
			if k == PrefPagePrefs {
				if err := validPagePrefs(v); err != nil {
					return nil, err
				}
				continue
			}
			if _, isStr := v.(string); v != nil && !isStr {
				return nil, fmt.Errorf("%w: %q must be a string or null", ErrUnknownPreferenceKey, k)
			}
		}
		if v, ok := req.Preferences[PrefThemeMode]; ok && v != nil {
			if str, _ := v.(string); !ThemeModes[str] {
				return nil, fmt.Errorf("%w: %q is not a theme mode", ErrUnknownPreferenceKey, str)
			}
		}
		if v, ok := req.Preferences[PrefLanguage]; ok && v != nil {
			if str, _ := v.(string); !Languages[str] {
				return nil, fmt.Errorf("%w: %q is not a language", ErrUnknownPreferenceKey, str)
			}
		}
		for _, k := range []string{PrefThemeID, PrefFontTH, PrefFontEN} {
			if v, ok := req.Preferences[k]; ok && v != nil {
				if n := utf8.RuneCountInString(v.(string)); n < 1 || n > maxAppIDLength {
					return nil, fmt.Errorf("%w: %q must be 1-%d characters", ErrUnknownPreferenceKey, k, maxAppIDLength)
				}
			}
		}
		if v, ok := req.Preferences[PrefMoneyStyle]; ok && v != nil {
			if str, _ := v.(string); !MoneyStyles[str] {
				return nil, fmt.Errorf("%w: %q is not a money style", ErrUnknownPreferenceKey, str)
			}
		}
		if v, ok := req.Preferences[PrefMoneyDecimals]; ok && v != nil {
			if str, _ := v.(string); !MoneyDecimals[str] {
				return nil, fmt.Errorf("%w: %q is not a decimals mode", ErrUnknownPreferenceKey, str)
			}
		}
		if v, ok := req.Preferences["timezone"]; ok && v != nil {
			if str, _ := v.(string); !ValidTimezone(str) {
				return nil, fmt.Errorf("%w: %q is not a timezone", ErrUnknownPreferenceKey, str)
			}
		}
		if v, ok := req.Preferences[PrefFeeCategoryID]; ok && v != nil {
			str, _ := v.(string)
			catID, err := uuid.Parse(str)
			if err != nil {
				return nil, ErrInvalidFeeCategory
			}
			if s.feeCategoryCheck != nil {
				if err := s.feeCategoryCheck(ctx, id, catID); err != nil {
					return nil, ErrInvalidFeeCategory
				}
			}
		}
	}
	if req.AvatarSettings != nil {
		if err := normalizeAvatarSettings(req.AvatarSettings); err != nil {
			return nil, err
		}
	}
	if err := s.store.UpdateProfile(ctx, id, req); err != nil {
		return nil, err
	}
	return s.store.GetProfile(ctx, id)
}

// ErrInvalidAvatarSettings — a 400 on avatar_settings.
var ErrInvalidAvatarSettings = errors.New("invalid avatar_settings")

const (
	maxAvatarColors     = 8
	maxAvatarColorChars = 32
)

// normalizeAvatarSettings checks the shape and fills empty lists (the source's availability is
// checked in the store, inside the write).
func normalizeAvatarSettings(a *shared.AvatarSettings) error {
	if a.Source != shared.AvatarSourceIcon && !shared.AvatarSources[a.Source] {
		return fmt.Errorf("%w: source %q", ErrInvalidAvatarSettings, a.Source)
	}
	// only set colours are kept: empty / null entries are dropped (an unset slot is just missing)
	check := func(field string, colors []string) ([]string, error) {
		out := shared.CleanColors(colors)
		if len(out) > maxAvatarColors {
			return nil, fmt.Errorf("%w: %s holds at most %d colours", ErrInvalidAvatarSettings, field, maxAvatarColors)
		}
		for _, c := range out {
			if utf8.RuneCountInString(c) > maxAvatarColorChars {
				return nil, fmt.Errorf("%w: %s entries are at most %d characters", ErrInvalidAvatarSettings, field, maxAvatarColorChars)
			}
		}
		return out, nil
	}
	var err error
	if a.Colors, err = check("colors", a.Colors); err != nil {
		return err
	}
	if a.Border != nil {
		if !shared.AvatarBorderStyles[a.Border.Style] {
			return fmt.Errorf("%w: border.style is thin, thick or dashed (no border = null)", ErrInvalidAvatarSettings)
		}
		if a.Border.Colors, err = check("border.colors", a.Border.Colors); err != nil {
			return err
		}
	}
	return nil
}

// DeleteAvatar — DELETE /avatars/{source}: my stored picture of that source goes; the pick falls
// back to the icon when it was picked.
func (s *Service) DeleteAvatar(ctx context.Context, id uuid.UUID, source string) (*Profile, error) {
	if !shared.AvatarSources[source] {
		return nil, fmt.Errorf("%w: source %q", ErrInvalidAvatarSettings, source)
	}
	if err := s.store.DeleteAvatar(ctx, id, source); err != nil {
		return nil, err
	}
	return s.store.GetProfile(ctx, id)
}

// validPagePrefs — page_prefs: null (reset every page) or an object keyed by known page ids, each
// entry null (reset that page) or an object of at most maxPagePrefBytes as JSON. Inner keys are the
// app's own.
func validPagePrefs(v any) error {
	if v == nil {
		return nil
	}
	pages, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: page_prefs must be an object or null", ErrUnknownPreferenceKey)
	}
	for page, entry := range pages {
		if !PageIDs[page] {
			return fmt.Errorf("%w: page_prefs.%s", ErrUnknownPreferenceKey, page)
		}
		if entry == nil {
			continue
		}
		if _, ok := entry.(map[string]any); !ok {
			return fmt.Errorf("%w: page_prefs.%s must be an object or null", ErrUnknownPreferenceKey, page)
		}
		if b, err := json.Marshal(entry); err != nil || len(b) > maxPagePrefBytes {
			return fmt.Errorf("%w: page_prefs.%s is over %d bytes", ErrUnknownPreferenceKey, page, maxPagePrefBytes)
		}
	}
	return nil
}

func (s *Service) Deactivate(ctx context.Context, id uuid.UUID) error {
	return s.store.SetStatus(ctx, id, "inactive")
}

func (s *Service) Reactivate(ctx context.Context, id uuid.UUID, currentStatus string) error {
	if currentStatus == "active" {
		return ErrAlreadyActive
	}
	return s.store.SetStatus(ctx, id, "active")
}
