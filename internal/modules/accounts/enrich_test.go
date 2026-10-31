package accounts

import (
	"testing"
	"time"
)

func TestNextDayOfMonthAtMonthEnd(t *testing.T) {
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		today time.Time
		due   int
		want  time.Time
	}{
		{d(2026, 10, 31), 20, d(2026, 11, 20)}, // November has no 31st
		{d(2026, 1, 31), 5, d(2026, 2, 5)},
		{d(2026, 1, 30), 31, d(2026, 1, 31)},
		{d(2026, 12, 31), 20, d(2027, 1, 20)},
	}
	for _, c := range cases {
		if got := nextDayOfMonth(c.today, c.due); !got.Equal(c.want) {
			t.Errorf("nextDayOfMonth(%s, %d) = %s, want %s", c.today.Format("2006-01-02"), c.due, got.Format("2006-01-02"), c.want.Format("2006-01-02"))
		}
	}
}
