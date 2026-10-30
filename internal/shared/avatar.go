package shared

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Avatar is how to draw a person (0.4.2 1b): one object on every person payload, resolved here so
// the app never picks the source. Picked source when available → the icon → initials.
type Avatar struct {
	Kind     string        `json:"kind"` // photo | icon | initials
	PhotoURL *string       `json:"photo_url"`
	IconCode *IconCode     `json:"icon_code"`
	Initials string        `json:"initials"`
	Colors   []string      `json:"colors"` // profile colours in order; never empty
	Border   *AvatarBorder `json:"border"` // photo only
}

type AvatarBorder struct {
	Style  string   `json:"style"` // thin | thick | dashed (AvatarBorderStyles); no border = a null border
	Colors []string `json:"colors"`
}

// AvatarBorderStyles are the icon-maker border ids a photo ring may use.
var AvatarBorderStyles = map[string]bool{"thin": true, "thick": true, "dashed": true}

// AvatarSettings is users.avatar_settings (migration 122).
type AvatarSettings struct {
	Source string        `json:"source"` // icon | upload | telegram | line | google
	Colors []string      `json:"colors"`
	Border *AvatarBorder `json:"border"`
}

const (
	AvatarPhoto    = "photo"
	AvatarIcon     = "icon"
	AvatarInitials = "initials"

	AvatarSourceIcon = "icon"
)

// AvatarSources are the picture sources a user can store (users.avatar_settings.source besides icon).
var AvatarSources = map[string]bool{"upload": true, "telegram": true, "line": true, "google": true}

// DefaultAvatarSettings is the column default.
func DefaultAvatarSettings() AvatarSettings {
	return AvatarSettings{Source: AvatarSourceIcon, Colors: []string{}}
}

// AvatarSQL is the avatar_src() call (migration 122) for a person: userExpr = the app user's id
// (NULL for a typed name / an unlinked contact), iconExpr = their own icon_code (a contact's,
// a member's) used when the user has none. Scan it into []byte and pass it to AvatarFromSrc.
func AvatarSQL(userExpr, iconExpr string) string {
	return fmt.Sprintf(`avatar_src(%s, %s)`, userExpr, iconExpr)
}

// PersonAvatarSQL is AvatarSQL for a contact (alias c) and its linked user (alias lu).
func PersonAvatarSQL(contactAlias, linkedUserAlias string) string {
	return AvatarSQL(linkedUserAlias+".id", contactAlias+".icon_code")
}

// avatarSrc is what avatar_src() returns.
type avatarSrc struct {
	IconCode     *IconCode       `json:"icon_code"`
	Settings     *AvatarSettings `json:"settings"`
	PhotoID      *uuid.UUID      `json:"photo_id"`
	PhotoVersion int             `json:"photo_version"`
}

// AvatarFromSrc builds the avatar of a person named name from an avatar_src() value (nil / bad
// JSON = initials).
func AvatarFromSrc(name string, raw []byte) Avatar {
	var src avatarSrc
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &src)
	}
	return NewAvatar(name, src.IconCode, src.Settings, src.PhotoID, src.PhotoVersion)
}

// AvatarFromIcon is the avatar of someone who is not an app user (a typed name, a contact's own
// icon).
func AvatarFromIcon(name string, icon *IconCode) Avatar {
	return NewAvatar(name, icon, nil, nil, 0)
}

// NewAvatar resolves the avatar. photoID = the picked source's picture when it can be shown
// (avatar_src already checked the source and the link).
func NewAvatar(name string, icon *IconCode, settings *AvatarSettings, photoID *uuid.UUID, version int) Avatar {
	if !iconDrawable(icon) {
		icon = nil
	}
	a := Avatar{Initials: Initials(name), IconCode: icon}
	switch {
	case photoID != nil && settings != nil && settings.Source != AvatarSourceIcon:
		url := AvatarPhotoURL(*photoID, version)
		a.Kind, a.PhotoURL = AvatarPhoto, &url
		var border []string
		if settings.Border != nil {
			border = CleanColors(settings.Border.Colors)
			a.Border = &AvatarBorder{Style: settings.Border.Style, Colors: border}
		}
		// only the set colours, in order: avatar_settings.colors (main first), then the border's
		a.Colors = uniqueColors(settings.Colors, border)
		if len(a.Colors) == 0 && icon != nil {
			a.Colors = iconColors(icon)
		}
	case icon != nil:
		a.Kind = AvatarIcon
		a.Colors = iconColors(icon)
	default:
		a.Kind = AvatarInitials
	}
	if len(a.Colors) == 0 {
		a.Colors = []string{NameColor(name)}
	}
	return a
}

// AvatarPhotoURL is the photo_url of a stored picture (GET /api/v1/avatars/{id}?v=).
func AvatarPhotoURL(id uuid.UUID, version int) string {
	return fmt.Sprintf("/api/v1/avatars/%s?v=%d", id, version)
}

// iconDrawable: an icon_code with nothing to draw counts as no icon.
func iconDrawable(ic *IconCode) bool {
	return ic != nil && ((ic.Icon != nil && *ic.Icon != "") || (ic.Background != nil && *ic.Background != ""))
}

// iconColors: background colours, then icon colours, then border colours, duplicates removed.
func iconColors(ic *IconCode) []string {
	return uniqueColors(ic.BgColors, ic.IconColors, ic.BorderColors)
}

// CleanColors drops unset entries ("" / whitespace; a JSON null decodes to ""), keeping the order;
// never nil.
func CleanColors(colors []string) []string {
	out := make([]string, 0, len(colors))
	for _, c := range colors {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func uniqueColors(lists ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, l := range lists {
		for _, c := range l {
			c = strings.TrimSpace(c)
			k := strings.ToLower(c)
			if c == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, c)
		}
	}
	return out
}

// avatarPalette is the app's AppAvatar.hashPalette (lib/ui/atoms/app_avatar.dart), same order.
var avatarPalette = []string{
	"#E57373", "#F06292", "#BA68C8", "#9575CD", "#7986CB", "#64B5F6",
	"#4FC3F7", "#4DD0E1", "#AED581", "#FFD54F", "#FFB74D", "#A1887F",
}

// NameColor is the app's AppAvatar.colorFor: hash = hash*31 + rune over the name's runes,
// masked to 31 bits; "" → the first colour.
func NameColor(name string) string {
	if name == "" {
		return avatarPalette[0]
	}
	var hash int64
	for _, r := range name {
		hash = (hash*31 + int64(r)) & 0x7fffffff
	}
	return avatarPalette[hash%int64(len(avatarPalette))]
}

// Initials is the app's AppAvatar._initialsOf: the first grapheme of the first word (one word) or
// of the first two words, upper-cased.
func Initials(name string) string {
	words := strings.Fields(name)
	if len(words) == 0 {
		return ""
	}
	if len(words) == 1 {
		return strings.ToUpper(firstGrapheme(words[0]))
	}
	return strings.ToUpper(firstGrapheme(words[0]) + firstGrapheme(words[1]))
}

// firstGrapheme approximates the first user-perceived character: a rune plus the combining marks,
// variation selectors, emoji modifiers and zero-width-joined runes that follow it (enough for
// Thai, Latin and emoji names).
func firstGrapheme(s string) string {
	if s == "" {
		return ""
	}
	_, n := utf8.DecodeRuneInString(s)
	for n < len(s) {
		r, size := utf8.DecodeRuneInString(s[n:])
		switch {
		case unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc),
			r >= 0xFE00 && r <= 0xFE0F, r >= 0x1F3FB && r <= 0x1F3FF:
			n += size
		case r == 0x200D:
			n += size
			if n < len(s) {
				_, next := utf8.DecodeRuneInString(s[n:])
				n += next
			}
		default:
			return s[:n]
		}
	}
	return s[:n]
}
