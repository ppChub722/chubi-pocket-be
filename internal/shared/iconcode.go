package shared

import "encoding/json"

type IconCode struct {
	Icon         *string  `json:"icon"`
	IconColors   []string `json:"iconColors"`
	Background   *string  `json:"background"`
	BgColors     []string `json:"bgColors"`
	Border       *string  `json:"border"`
	BorderColors []string `json:"borderColors"`
	Shape        *string  `json:"shape,omitempty"`
}

var iconShapes = map[string]bool{
	"circle": true, "squircle": true, "rounded": true,
	"square": true, "leaf": true, "drop": true,
}

// UnmarshalJSON decodes as usual, then drops a Shape outside the allowlist (so a bad client value
// can never persist) — nil renders as a circle.
func (ic *IconCode) UnmarshalJSON(data []byte) error {
	type plain IconCode // no methods → no recursion
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.Shape != nil && !iconShapes[*p.Shape] {
		p.Shape = nil
	}
	*ic = IconCode(p)
	return nil
}
