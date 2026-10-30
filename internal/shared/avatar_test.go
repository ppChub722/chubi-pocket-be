package shared

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// Reference values printed by the app's own code (AppAvatar.colorFor / _initialsOf, dart +
// package:characters, 2026-10-28).
func TestNameColorAndInitialsMatchTheApp(t *testing.T) {
	cases := []struct{ name, color, initials string }{
		{"", "#E57373", ""},
		{"Mom", "#9575CD", "M"},
		{"Beam", "#4DD0E1", "B"},
		{"ppond", "#64B5F6", "P"},
		{"น้อง", "#BA68C8", "น้"},
		{"สมชาย ใจดี", "#9575CD", "สใ"},
		{"เอ", "#64B5F6", "เ"},
		{"john smith", "#FFB74D", "JS"},
		{"  lee  ", "#7986CB", "L"},
		{"Ángel ñu", "#A1887F", "ÁÑ"},
		{"👨‍👩‍👧 fam", "#AED581", "👨‍👩‍👧F"},
		{"ปั้น", "#BA68C8", "ปั้"},
		{"A very long name with many words", "#BA68C8", "AV"},
		{"ฉัน", "#64B5F6", "ฉั"},
	}
	for _, c := range cases {
		if got := NameColor(c.name); got != c.color {
			t.Errorf("NameColor(%q) = %s, want %s", c.name, got, c.color)
		}
		if got := Initials(c.name); got != c.initials {
			t.Errorf("Initials(%q) = %q, want %q", c.name, got, c.initials)
		}
	}
}

func str(s string) *string { return &s }

func TestNewAvatarFallbackChain(t *testing.T) {
	icon := &IconCode{Icon: str("face"), BgColors: []string{"#111111", "#222222"},
		IconColors: []string{"#222222", "primary"}, BorderColors: []string{"#333333"}}
	photo := uuid.MustParse("0191c2a0-0000-7000-8000-0000000000cc")
	tg := &AvatarSettings{Source: "telegram", Colors: []string{"#E8A33D"},
		Border: &AvatarBorder{Style: "thick", Colors: []string{"#F06292", "#e8a33d"}}}

	// photo picked + available
	a := NewAvatar("Nong Lee", icon, tg, &photo, 3)
	if a.Kind != AvatarPhoto || a.PhotoURL == nil || *a.PhotoURL != "/api/v1/avatars/"+photo.String()+"?v=3" {
		t.Errorf("photo = %+v", a)
	}
	if !reflect.DeepEqual(a.Colors, []string{"#E8A33D", "#F06292"}) || a.Border == nil || a.IconCode != icon || a.Initials != "NL" {
		t.Errorf("photo colours / border / fallback = %+v", a)
	}

	// photo picked but not available (no photo id) → icon; colours from the icon, deduped
	a = NewAvatar("Nong", icon, tg, nil, 0)
	if a.Kind != AvatarIcon || a.PhotoURL != nil || a.Border != nil {
		t.Errorf("icon fallback = %+v", a)
	}
	if !reflect.DeepEqual(a.Colors, []string{"#111111", "#222222", "primary", "#333333"}) {
		t.Errorf("icon colours = %v", a.Colors)
	}

	// only set colours, in order: unset ("" / null) slots are simply missing
	a = NewAvatar("Nong", nil, &AvatarSettings{Source: "upload", Colors: []string{"#E8A33D", "", " "}}, &photo, 1)
	if !reflect.DeepEqual(a.Colors, []string{"#E8A33D"}) || a.Border != nil {
		t.Errorf("main colour only = %+v", a)
	}
	a = NewAvatar("Nong", nil, &AvatarSettings{Source: "upload", Colors: []string{"", "#E8A33D"},
		Border: &AvatarBorder{Style: "dashed", Colors: []string{"", "#F06292", "", "#4DD0E1"}}}, &photo, 1)
	if !reflect.DeepEqual(a.Colors, []string{"#E8A33D", "#F06292", "#4DD0E1"}) || a.Border == nil ||
		a.Border.Style != "dashed" || !reflect.DeepEqual(a.Border.Colors, []string{"#F06292", "#4DD0E1"}) {
		t.Errorf("main + border colours = %+v / %+v", a, a.Border)
	}
	a = NewAvatar("Mom", nil, &AvatarSettings{Source: "upload", Colors: []string{"", ""}}, &photo, 1)
	if !reflect.DeepEqual(a.Colors, []string{"#9575CD"}) {
		t.Errorf("no colours set = %v, want the name colour", a.Colors)
	}
	a = NewAvatar("Mom", &IconCode{Icon: str("face"), BgColors: []string{"", "#111111"}, IconColors: []string{""}}, nil, nil, 0)
	if !reflect.DeepEqual(a.Colors, []string{"#111111"}) {
		t.Errorf("icon with empty slots = %v", a.Colors)
	}
	if got := CleanColors(nil); got == nil || len(got) != 0 {
		t.Errorf("CleanColors(nil) = %#v, want []", got)
	}

	// photo with no colours of its own → the icon's colours
	a = NewAvatar("Nong", icon, &AvatarSettings{Source: "upload"}, &photo, 1)
	if a.Kind != AvatarPhoto || a.Colors[0] != "#111111" {
		t.Errorf("photo without colours = %+v", a)
	}

	// no icon → initials on the name colour
	a = NewAvatar("Mom", nil, tg, nil, 0)
	if a.Kind != AvatarInitials || !reflect.DeepEqual(a.Colors, []string{"#9575CD"}) || a.IconCode != nil {
		t.Errorf("initials = %+v", a)
	}

	// an icon with nothing to draw counts as none
	a = NewAvatar("Mom", &IconCode{BgColors: []string{"#000000"}}, nil, nil, 0)
	if a.Kind != AvatarInitials || a.IconCode != nil {
		t.Errorf("empty icon = %+v", a)
	}

	// an icon without colours → the name colour
	a = NewAvatar("Mom", &IconCode{Icon: str("face")}, nil, nil, 0)
	if a.Kind != AvatarIcon || !reflect.DeepEqual(a.Colors, []string{"#9575CD"}) {
		t.Errorf("colourless icon = %+v", a)
	}
}

func TestAvatarFromSrc(t *testing.T) {
	raw := []byte(`{"icon_code":{"icon":"face","bgColors":["#123456"]},"settings":{"source":"telegram","colors":["#ABCDEF"],"border":null},"photo_id":"0191c2a0-0000-7000-8000-0000000000cc","photo_version":2}`)
	a := AvatarFromSrc("Beam", raw)
	if a.Kind != AvatarPhoto || *a.PhotoURL != "/api/v1/avatars/0191c2a0-0000-7000-8000-0000000000cc?v=2" ||
		!reflect.DeepEqual(a.Colors, []string{"#ABCDEF"}) {
		t.Errorf("photo src = %+v", a)
	}
	a = AvatarFromSrc("Beam", []byte(`{"icon_code":null}`))
	if a.Kind != AvatarInitials || a.Initials != "B" {
		t.Errorf("null src = %+v", a)
	}
	if a := AvatarFromSrc("Beam", nil); a.Kind != AvatarInitials {
		t.Errorf("nil src = %+v", a)
	}
	b, _ := json.Marshal(AvatarFromSrc("", nil))
	if string(b) != `{"kind":"initials","photo_url":null,"icon_code":null,"initials":"","colors":["#E57373"],"border":null}` {
		t.Errorf("json = %s", b)
	}
}
