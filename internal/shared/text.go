package shared

import (
	"encoding/json"
	"strings"
)

const (
	NameMax        = 100
	DescriptionMax = 200
	NoteMax        = 500
)

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

func TextChange(v *string, present bool) (*string, bool) {
	return CleanText(v), present || v != nil
}
