package shared

import "encoding/json"

// IconCode is the JSONB recipe stored for all icon-bearing entities.
// Art IDs (icon, background, border) are resolved by the FE art registry —
// they are never stored as file paths. Each art piece declares colorSlots
// (0–3) driving the dynamic color-picker UI; an empty slice means the FE
// uses its own default rendering color.
//
// Shape is the background outline (circle, squircle, rounded, square, leaf,
// drop); nil = circle, so rows saved before shapes existed are unchanged.
// Unknown values are dropped to nil on decode. Background is texture art that
// fills the shape. Border is optional art drawn along the shape's edge.
//
// DB encoding: stored as JSONB. Callers marshal/unmarshal via json.Marshal /
// json.Unmarshal and pass the []byte to pgx with a ::jsonb cast. Scanning
// uses a *[]byte destination — see store helpers in each module.
type IconCode struct {
	Icon         *string  `json:"icon"`
	IconColors   []string `json:"iconColors"`
	Background   *string  `json:"background"`
	BgColors     []string `json:"bgColors"`
	Border       *string  `json:"border"`
	BorderColors []string `json:"borderColors"`
	Shape        *string  `json:"shape,omitempty"`
}

// iconShapes is the allowlist for IconCode.Shape (mirrors the app's IconShape).
var iconShapes = map[string]bool{
	"circle": true, "squircle": true, "rounded": true,
	"square": true, "leaf": true, "drop": true,
}

// UnmarshalJSON decodes as usual, then drops a Shape outside the allowlist
// (so a bad client value can never persist) — nil renders as a circle.
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
