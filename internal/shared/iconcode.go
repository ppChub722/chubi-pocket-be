package shared

// IconCode is the JSONB recipe stored for all icon-bearing entities.
// Art IDs (icon, background, border) are resolved by the FE art registry —
// they are never stored as file paths. Each art piece declares colorSlots
// (0–3) driving the dynamic color-picker UI; an empty slice means the FE
// uses its own default rendering color.
//
// Shape is always a circle (not stored). Background is texture art that fills
// the circle. Border is optional art drawn on top of the circle's edge.
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
}
