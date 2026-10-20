package recurrence

import (
	"fmt"
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func ptr[T any](v T) *T { return &v }

// betweenCase is one schedule; the end (none / date / count) is worked out by
// Limits as /recurring does, so GeneratedUntil is set like in production.
type betweenCase struct {
	name string
	spec Spec
	// windows are the [from, to] pairs checked on top of the sweep.
	windows [][2]time.Time
}

func cases(t *testing.T) []betweenCase {
	monthDay := func(d int) Spec {
		return Spec{Freq: "monthly", Interval: 1, MonthlyMode: ptr("day"), MonthDay: ptr(d)}
	}
	nth := func(n, wd, interval int) Spec {
		return Spec{Freq: "monthly", Interval: interval, MonthlyMode: ptr("weekday"), MonthNth: ptr(n), MonthWeekday: ptr(wd)}
	}
	with := func(s Spec, start time.Time, kind string, mod func(*Spec)) Spec {
		s.StartDate = start
		s.EndKind = kind
		if mod != nil {
			mod(&s)
		}
		return s
	}
	cs := []betweenCase{
		{name: "daily 1", spec: with(Spec{Freq: "daily", Interval: 1}, day(2026, 1, 1), "none", nil),
			windows: [][2]time.Time{{day(2026, 3, 10), day(2026, 3, 20)}, {day(2025, 12, 20), day(2026, 1, 5)}, {day(2025, 11, 1), day(2025, 12, 31)}}},
		{name: "daily 3 start long ago", spec: with(Spec{Freq: "daily", Interval: 3}, day(2025, 6, 1), "none", nil),
			windows: [][2]time.Time{{day(2026, 9, 1), day(2026, 10, 15)}, {day(2025, 12, 28), day(2026, 1, 4)}}},
		{name: "daily 1 start 2.5 years back", spec: with(Spec{Freq: "daily", Interval: 1}, day(2024, 3, 15), "none", nil),
			windows: [][2]time.Time{{day(2026, 10, 1), day(2026, 10, 31)}}},
		{name: "weekly default weekday", spec: with(Spec{Freq: "weekly", Interval: 1}, day(2026, 2, 4), "none", nil),
			windows: [][2]time.Time{{day(2026, 2, 1), day(2026, 3, 31)}, {day(2026, 2, 4), day(2026, 2, 4)}}},
		{name: "weekly mon wed fri", spec: with(Spec{Freq: "weekly", Interval: 1, Weekdays: []int{5, 1, 3}}, day(2026, 2, 4), "none", nil),
			windows: [][2]time.Time{{day(2026, 3, 2), day(2026, 3, 31)}, {day(2026, 2, 1), day(2026, 2, 9)}}},
		{name: "weekly every 3 weeks tue sun across a year", spec: with(Spec{Freq: "weekly", Interval: 3, Weekdays: []int{2, 7}}, day(2024, 1, 10), "none", nil),
			windows: [][2]time.Time{{day(2025, 12, 15), day(2026, 1, 20)}}},
		{name: "weekly weekdays before the start in week one", spec: with(Spec{Freq: "weekly", Interval: 1, Weekdays: []int{1, 4, 6}}, day(2026, 1, 8), "none", nil),
			windows: [][2]time.Time{{day(2026, 1, 5), day(2026, 1, 25)}}},
		{name: "weekly start long ago", spec: with(Spec{Freq: "weekly", Interval: 2, Weekdays: []int{3}}, day(2019, 5, 1), "none", nil),
			windows: [][2]time.Time{{day(2026, 9, 1), day(2026, 11, 30)}}},
		{name: "monthly day 15 start years back", spec: with(monthDay(15), day(2020, 1, 15), "none", nil),
			windows: [][2]time.Time{{day(2026, 10, 1), day(2026, 12, 31)}, {day(2025, 12, 16), day(2026, 1, 14)}}},
		{name: "monthly day 31 clamps", spec: with(monthDay(31), day(2025, 1, 31), "none", nil),
			windows: [][2]time.Time{{day(2026, 1, 1), day(2026, 6, 30)}}},
		{name: "monthly start day 31 every 3 months", spec: with(Spec{Freq: "monthly", Interval: 3}, day(2024, 1, 31), "none", nil),
			windows: [][2]time.Time{{day(2025, 6, 1), day(2026, 12, 31)}}},
		{name: "monthly day before the start day", spec: with(monthDay(5), day(2026, 1, 20), "none", nil),
			windows: [][2]time.Time{{day(2026, 1, 1), day(2026, 4, 30)}}},
		{name: "monthly 2nd wednesday", spec: with(nth(2, 3, 1), day(2025, 3, 1), "none", nil),
			windows: [][2]time.Time{{day(2026, 1, 1), day(2026, 12, 31)}}},
		{name: "monthly last friday every 2 months", spec: with(nth(-1, 5, 2), day(2024, 5, 1), "none", nil),
			windows: [][2]time.Time{{day(2025, 11, 1), day(2026, 3, 31)}}},
		{name: "monthly 5th monday skips short months", spec: with(nth(5, 1, 1), day(2025, 1, 1), "none", nil),
			windows: [][2]time.Time{{day(2026, 1, 1), day(2026, 12, 31)}}},
		{name: "yearly feb 29", spec: with(Spec{Freq: "yearly", Interval: 1}, day(2020, 2, 29), "none", nil),
			windows: [][2]time.Time{{day(2023, 1, 1), day(2029, 12, 31)}, {day(2027, 3, 1), day(2028, 2, 28)}}},
		{name: "yearly every 3 years", spec: with(Spec{Freq: "yearly", Interval: 3}, day(2010, 6, 15), "none", nil),
			windows: [][2]time.Time{{day(2025, 1, 1), day(2037, 12, 31)}}},
		{name: "yearly across a year boundary", spec: with(Spec{Freq: "yearly", Interval: 1}, day(2024, 12, 31), "none", nil),
			windows: [][2]time.Time{{day(2025, 12, 25), day(2026, 1, 5)}}},
		{name: "dates", spec: with(Spec{Freq: "dates", Interval: 1, Dates: []time.Time{day(2026, 7, 1), day(2026, 1, 10), day(2026, 3, 5), day(2026, 3, 5)}}, day(2026, 1, 1), "none", nil),
			windows: [][2]time.Time{{day(2026, 2, 1), day(2026, 7, 1)}, {day(2026, 1, 10), day(2026, 1, 10)}, {day(2026, 7, 2), day(2026, 8, 1)}}},
		{name: "count end weekly", spec: with(Spec{Freq: "weekly", Interval: 1, Weekdays: []int{1, 4}}, day(2026, 1, 5), "count", func(s *Spec) { s.EndCount = ptr(10) }),
			windows: [][2]time.Time{{day(2026, 1, 1), day(2026, 1, 31)}, {day(2026, 2, 1), day(2026, 12, 31)}, {day(2026, 2, 5), day(2026, 2, 5)}}},
		{name: "count end monthly day 31", spec: with(Spec{Freq: "monthly", Interval: 1}, day(2024, 1, 31), "count", func(s *Spec) { s.EndCount = ptr(6) }),
			windows: [][2]time.Time{{day(2024, 3, 1), day(2024, 12, 31)}, {day(2024, 6, 30), day(2024, 6, 30)}}},
		{name: "count end daily every 3 days", spec: with(Spec{Freq: "daily", Interval: 3}, day(2026, 1, 1), "count", func(s *Spec) { s.EndCount = ptr(40) }),
			windows: [][2]time.Time{{day(2026, 3, 1), day(2026, 6, 30)}}},
		{name: "count end yearly", spec: with(Spec{Freq: "yearly", Interval: 1}, day(2024, 2, 29), "count", func(s *Spec) { s.EndCount = ptr(3) }),
			windows: [][2]time.Time{{day(2024, 1, 1), day(2030, 12, 31)}, {day(2026, 1, 1), day(2026, 12, 31)}}},
		{name: "date end daily", spec: with(Spec{Freq: "daily", Interval: 1}, day(2026, 1, 1), "date", func(s *Spec) { s.EndDate = ptr(day(2026, 4, 30)) }),
			windows: [][2]time.Time{{day(2026, 4, 20), day(2026, 5, 20)}, {day(2026, 5, 1), day(2026, 5, 20)}}},
		{name: "date end monthly", spec: with(monthDay(15), day(2025, 11, 15), "date", func(s *Spec) { s.EndDate = ptr(day(2026, 6, 15)) }),
			windows: [][2]time.Time{{day(2026, 5, 1), day(2026, 8, 1)}, {day(2026, 6, 15), day(2026, 6, 15)}}},
		{name: "date end weekly intervals of 3", spec: with(Spec{Freq: "weekly", Interval: 3, Weekdays: []int{2, 6}}, day(2026, 1, 6), "date", func(s *Spec) { s.EndDate = ptr(day(2026, 8, 31)) }),
			windows: [][2]time.Time{{day(2026, 7, 1), day(2026, 9, 30)}}},
	}
	for i := range cs {
		if err := cs[i].spec.Limits(day(2026, 1, 1)); err != nil {
			t.Fatalf("%s: Limits: %v", cs[i].name, err)
		}
	}
	return cs
}

func sameDates(a, b []time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}

func show(ds []time.Time) string {
	s := make([]string, len(ds))
	for i, d := range ds {
		s[i] = d.Format(DateLayout)
	}
	return fmt.Sprint(s)
}

// want is the reference: every occurrence inside the end, from the start,
// kept when it falls in [from, to].
func want(s *Spec, from, to time.Time) []time.Time {
	out := []time.Time{}
	for _, d := range s.Within(to) {
		if !d.Before(from) && !d.After(to) {
			out = append(out, d)
		}
	}
	return out
}

func check(t *testing.T, name string, s *Spec, from, to time.Time) {
	t.Helper()
	got := s.Between(from, to)
	exp := want(s, from, to)
	if !sameDates(got, exp) {
		t.Errorf("%s [%s .. %s]\n got %s\nwant %s", name, from.Format(DateLayout), to.Format(DateLayout), show(got), show(exp))
	}
}

func TestBetweenEqualsOccurrencesFiltered(t *testing.T) {
	for _, c := range cases(t) {
		s := c.spec
		for _, w := range c.windows {
			check(t, c.name, &s, w[0], w[1])
		}
	}
}

// A sweep of windows around and after the start, so every period edge of
// every rule is met by some window.
func TestBetweenSweep(t *testing.T) {
	lengths := []int{0, 1, 6, 31, 100, 400}
	for _, c := range cases(t) {
		s := c.spec
		for off := -40; off <= 560; off += 13 {
			from := s.StartDate.AddDate(0, 0, off)
			for _, n := range lengths {
				to := from.AddDate(0, 0, n)
				if dayDiff(s.StartDate, to) > 950 {
					continue
				}
				check(t, c.name, &s, from, to)
			}
		}
	}
}

func TestBetweenEmptyAndEdges(t *testing.T) {
	s := Spec{Freq: "daily", Interval: 1, StartDate: day(2026, 1, 1), EndKind: "none"}
	if got := s.Between(day(2026, 2, 1), day(2026, 1, 1)); len(got) != 0 {
		t.Errorf("from after to: got %s", show(got))
	}
	if got := s.Between(day(2025, 1, 1), day(2025, 12, 31)); len(got) != 0 {
		t.Errorf("window before the start: got %s", show(got))
	}
	if got := s.Between(day(2026, 1, 1), day(2026, 1, 1)); !sameDates(got, []time.Time{day(2026, 1, 1)}) {
		t.Errorf("the start day itself: got %s", show(got))
	}
	// a time of day on the arguments does not move the window
	from := time.Date(2026, 1, 3, 17, 30, 0, 0, time.UTC)
	to := time.Date(2026, 1, 4, 8, 0, 0, 0, time.UTC)
	if got := s.Between(from, to); !sameDates(got, []time.Time{day(2026, 1, 3), day(2026, 1, 4)}) {
		t.Errorf("times of day: got %s", show(got))
	}
	// an interval of 0 is read as 1 and must end
	z := Spec{Freq: "daily", Interval: 0, StartDate: day(2026, 1, 1), EndKind: "none"}
	if got := z.Between(day(2026, 1, 1), day(2026, 1, 3)); len(got) != 3 {
		t.Errorf("interval 0: got %s", show(got))
	}
}

// A rule that began long ago: Occurrences stops at its 1000-date cap, Between
// must not.
func TestBetweenPastTheOccurrencesCap(t *testing.T) {
	s := Spec{Freq: "daily", Interval: 1, StartDate: day(2015, 1, 1), EndKind: "none"}
	got := s.Between(day(2026, 10, 1), day(2026, 10, 3))
	if exp := []time.Time{day(2026, 10, 1), day(2026, 10, 2), day(2026, 10, 3)}; !sameDates(got, exp) {
		t.Errorf("daily from 2015: got %s", show(got))
	}
	// a count end counts from the start, not from the jump: the 3rd of a
	// yearly rule is 2026 whatever the window starts at
	end := Spec{Freq: "yearly", Interval: 1, StartDate: day(2024, 6, 1), EndKind: "count", EndCount: ptr(3)}
	if got := end.Between(day(2025, 1, 1), day(2035, 1, 1)); !sameDates(got, []time.Time{day(2025, 6, 1), day(2026, 6, 1)}) {
		t.Errorf("count end without GeneratedUntil: got %s", show(got))
	}
	if got := end.Between(day(2027, 1, 1), day(2035, 1, 1)); len(got) != 0 {
		t.Errorf("after the 3rd occurrence: got %s", show(got))
	}
}
