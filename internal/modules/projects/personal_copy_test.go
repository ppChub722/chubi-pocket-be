package projects

import "testing"

func TestSuggestedUpdateKeepsBasis(t *testing.T) {
	note := "dinner"
	after := &ProjectTransaction{Amount: 1200, Date: "2026-10-08", Note: &note}
	cases := []struct {
		name       string
		copyAmount float64
		want       float64
	}{
		{"full copy follows the new total", 1000, 1200},
		{"share copy follows the new share", 400, 450},
		{"hand-edited copy keeps its amount", 333, 333},
	}
	for _, c := range cases {
		got := suggestedUpdate(c.copyAmount, 1000, 400, 1200, 450, after)
		if got.Amount != c.want || got.Date != "2026-10-08" || got.Note == nil || *got.Note != note {
			t.Errorf("%s: got %+v, want amount %v", c.name, got, c.want)
		}
	}
}
