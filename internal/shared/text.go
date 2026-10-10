package shared

import (
	"encoding/json"
	"strings"
)

// Text limits (owner standard 2026-10-10), in characters — the validator's
// max counts runes, so Thai text gets the full length. Used in binding tags
// as max=100 / max=200 / max=500; tags' name stays 50.
const (
	NameMax        = 100
	DescriptionMax = 200
	NoteMax        = 500
)

// CleanText trims a name / description / note. Blank → nil, so an empty
// string clears the column like an explicit null does.
func CleanText(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}

// DecodeTracked unmarshals data into v and reports which top-level keys
// the body carried — so an update can tell "absent" (leave alone) from
// `null` (clear). Call it from a request's UnmarshalJSON with an alias
// type to avoid recursion.
func DecodeTracked(data []byte, v any) (map[string]bool, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(probe))
	for k := range probe {
		present[k] = true
	}
	return present, nil
}

// TextChange — for a partial update: the cleaned value and whether to
// write it. A key the body carried (present) is written even when null or
// "" (= clear); a non-nil value set in Go by another module counts too.
func TextChange(v *string, present bool) (*string, bool) {
	return CleanText(v), present || v != nil
}
