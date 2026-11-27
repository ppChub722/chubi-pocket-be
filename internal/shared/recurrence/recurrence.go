// Package recurrence is the pure schedule engine behind /recurring items and
// the auto-saves of a saving goal: a rule (freq, interval, weekdays, monthly
// pattern, fixed dates) + a start date + an end, turned into dates.
package recurrence

import (
	"errors"
	"sort"
	"time"
)

var (
	// ErrInvalidRule — the rule can't produce dates (400 INVALID_RULE).
	ErrInvalidRule = errors.New("invalid recurrence rule")
	ErrEndTooFar   = errors.New("the end must be at most 2 years after the start")
)

const (
	DateLayout     = "2006-01-02"
	maxOccurrences = 1000 // safety stop for any loop
)

// LastDay in MonthDays = the month's last day (สิ้นเดือน).
const LastDay = -1

// Monthly — a monthly rule's pattern: mode day (the days in MonthDays) or weekday (the nth
// weekday; kept in the model, not offered on screen).
type Monthly struct {
	Mode    string `json:"mode"`
	Nth     *int   `json:"nth"`
	Weekday *int   `json:"weekday"`
}

// RecurrenceRule — the API shape. MonthDays: the days of a monthly (day mode) or yearly rule
// (1–31, -1 = the last day; a day past the month's end falls on its last day). Months: a yearly
// rule's months (1–12). Empty = the start date's day / month.
type RecurrenceRule struct {
	Freq      string   `json:"freq"`
	Interval  int      `json:"interval"`
	Weekdays  []int    `json:"weekdays"`
	Monthly   *Monthly `json:"monthly"`
	MonthDays []int    `json:"month_days"`
	Months    []int    `json:"months"`
	Dates     []string `json:"dates"`
}

type RecurrenceEnd struct {
	Kind  string  `json:"kind"`
	Date  *string `json:"date"`
	Count *int    `json:"count"`
}

// Spec is a schedule: the rule, where it starts and how it ends.
// TotalCount and GeneratedUntil are worked out by Limits.
type Spec struct {
	Freq           string
	Interval       int
	Weekdays       []int
	MonthlyMode    *string
	MonthDays      []int
	Months         []int
	MonthNth       *int
	MonthWeekday   *int
	Dates          []time.Time
	StartDate      time.Time
	EndKind        string
	EndDate        *time.Time
	EndCount       *int
	TotalCount     *int
	GeneratedUntil *time.Time
	// Gaps — spans the schedule skips (a setting resumed after a pause, 0.4.1-1 R8):
	// no occurrence is made in them, and a count end counts past them (R9).
	Gaps []Gap
}

// Gap — the occurrences d with From <= d < To are not made (YYYY-MM-DD).
type Gap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (r *Spec) inGap(d time.Time) bool {
	s := d.Format(DateLayout)
	for _, g := range r.Gaps {
		if s >= g.From && s < g.To {
			return true
		}
	}
	return false
}

// dropGaps keeps the dates outside every gap.
func (r *Spec) dropGaps(ds []time.Time) []time.Time {
	if len(r.Gaps) == 0 {
		return ds
	}
	out := ds[:0]
	for _, d := range ds {
		if !r.inGap(d) {
			out = append(out, d)
		}
	}
	return out
}

func ParseDate(s string) (time.Time, error) {
	return time.Parse(DateLayout, s)
}

func lastDay(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// IsoWeekday is Monday = 1 … Sunday = 7.
func IsoWeekday(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

// daysIn — the dates of month (y, m) for the day list days (empty = the start's day), sorted, each
// once: -1 and a day past the month's end fall on its last day.
func (r *Spec) daysIn(y int, m time.Month) []time.Time {
	days := r.MonthDays
	if len(days) == 0 {
		days = []int{r.StartDate.Day()}
	}
	ld := lastDay(y, m)
	seen := map[int]bool{}
	var nums []int
	for _, d := range days {
		if d == LastDay || d > ld {
			d = ld
		}
		if d < 1 || seen[d] {
			continue
		}
		seen[d] = true
		nums = append(nums, d)
	}
	sort.Ints(nums)
	out := make([]time.Time, 0, len(nums))
	for _, d := range nums {
		out = append(out, time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	}
	return out
}

// monthDates — the dates of a monthly rule in month (y, m) (none when its nth weekday is missing).
func (r *Spec) monthDates(y int, m time.Month) []time.Time {
	if r.MonthlyMode != nil && *r.MonthlyMode == "weekday" && r.MonthNth != nil && r.MonthWeekday != nil {
		if d, ok := nthWeekday(y, m, *r.MonthNth, *r.MonthWeekday); ok {
			return []time.Time{d}
		}
		return nil
	}
	return r.daysIn(y, m)
}

// yearDates — the dates of a yearly rule in year y (its months, else the start's month).
func (r *Spec) yearDates(y int) []time.Time {
	months := append([]int(nil), r.Months...)
	if len(months) == 0 {
		months = []int{int(r.StartDate.Month())}
	}
	sort.Ints(months)
	var out []time.Time
	for i, m := range months {
		if i > 0 && m == months[i-1] {
			continue
		}
		out = append(out, r.daysIn(y, time.Month(m))...)
	}
	return out
}

func nthWeekday(y int, m time.Month, nth, weekday int) (time.Time, bool) {
	if nth == -1 {
		d := time.Date(y, m, lastDay(y, m), 0, 0, 0, 0, time.UTC)
		for IsoWeekday(d) != weekday {
			d = d.AddDate(0, 0, -1)
		}
		return d, true
	}
	d := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	for IsoWeekday(d) != weekday {
		d = d.AddDate(0, 0, 1)
	}
	d = d.AddDate(0, 0, 7*(nth-1))
	return d, d.Month() == m
}

// Occurrences — the dates from the start to until, at most limit of them
// (0 = no limit). It ignores the end; Limits works that out.
func (r *Spec) Occurrences(until time.Time, limit int) []time.Time {
	out := []time.Time{}
	add := func(d time.Time) bool {
		if d.Before(r.StartDate) || d.After(until) {
			return !d.After(until)
		}
		if r.inGap(d) {
			return true
		}
		out = append(out, d)
		return limit == 0 || len(out) < limit
	}
	start := r.StartDate
	switch r.Freq {
	case "daily":
		for d := start; !d.After(until) && len(out) < maxOccurrences; d = d.AddDate(0, 0, r.Interval) {
			if !add(d) {
				break
			}
		}
	case "weekly":
		days := append([]int(nil), r.Weekdays...)
		if len(days) == 0 {
			days = []int{IsoWeekday(start)}
		}
		sort.Ints(days)
		weekStart := start.AddDate(0, 0, -(IsoWeekday(start) - 1))
	weeks:
		for w := weekStart; !w.After(until) && len(out) < maxOccurrences; w = w.AddDate(0, 0, 7*r.Interval) {
			for _, wd := range days {
				d := w.AddDate(0, 0, wd-1)
				if d.After(until) {
					break weeks
				}
				if !add(d) {
					break weeks
				}
			}
		}
	case "monthly":
	months:
		for k := 0; len(out) < maxOccurrences; k += r.Interval {
			first := time.Date(start.Year(), start.Month()+time.Month(k), 1, 0, 0, 0, 0, time.UTC)
			if first.After(until) {
				break
			}
			for _, d := range r.monthDates(first.Year(), first.Month()) {
				if !add(d) {
					break months
				}
			}
		}
	case "yearly":
	years:
		for k := 0; len(out) < maxOccurrences; k += r.Interval {
			y := start.Year() + k
			if time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).After(until) {
				break
			}
			for _, d := range r.yearDates(y) {
				if !add(d) {
					break years
				}
			}
		}
	case "dates":
		ds := append([]time.Time(nil), r.Dates...)
		sort.Slice(ds, func(i, j int) bool { return ds[i].Before(ds[j]) })
		for i, d := range ds {
			if i > 0 && d.Equal(ds[i-1]) {
				continue
			}
			if !add(d) {
				break
			}
		}
	}
	return out
}

func (r *Spec) HorizonSpan(from time.Time) time.Time {
	switch r.Freq {
	case "daily", "weekly":
		return from.AddDate(0, 3, 0)
	}
	return from.AddDate(1, 0, 0)
}

// MaxEnd — 2 years after the start, plus the time spent in gaps.
func (r *Spec) MaxEnd() time.Time {
	end := r.StartDate.AddDate(2, 0, 0)
	for _, g := range r.Gaps {
		f, err1 := ParseDate(g.From)
		t, err2 := ParseDate(g.To)
		if err1 == nil && err2 == nil && t.After(f) {
			end = end.Add(t.Sub(f))
		}
	}
	return end
}

func (r *Spec) Limits(today time.Time) error {
	switch r.Freq {
	case "dates":
		occ := r.Occurrences(r.MaxEnd(), 0)
		if len(occ) == 0 {
			return ErrInvalidRule
		}
		for _, d := range r.Dates {
			if d.After(r.MaxEnd()) || d.Before(r.StartDate) {
				return ErrEndTooFar
			}
		}
		n := len(occ)
		last := occ[n-1]
		r.EndKind, r.EndDate, r.EndCount = "none", nil, nil
		r.TotalCount, r.GeneratedUntil = &n, &last
		return nil
	}
	switch r.EndKind {
	case "date":
		if r.EndDate == nil || r.EndDate.Before(r.StartDate) {
			return ErrInvalidRule
		}
		if r.EndDate.After(r.MaxEnd()) {
			return ErrEndTooFar
		}
		n := len(r.Occurrences(*r.EndDate, 0))
		if n == 0 {
			return ErrInvalidRule
		}
		end := *r.EndDate
		r.TotalCount, r.GeneratedUntil, r.EndCount = &n, &end, nil
	case "count":
		if r.EndCount == nil || *r.EndCount < 1 {
			return ErrInvalidRule
		}
		occ := r.Occurrences(r.MaxEnd(), *r.EndCount)
		if len(occ) < *r.EndCount {
			return ErrEndTooFar
		}
		n := *r.EndCount
		last := occ[n-1]
		r.TotalCount, r.GeneratedUntil, r.EndDate = &n, &last, nil
	default:
		r.EndKind, r.EndDate, r.EndCount, r.TotalCount = "none", nil, nil, nil
		from := today
		if r.StartDate.After(from) {
			from = r.StartDate
		}
		h := r.HorizonSpan(from)
		if r.GeneratedUntil != nil && r.GeneratedUntil.After(h) {
			h = *r.GeneratedUntil
		}
		r.GeneratedUntil = &h
	}
	return nil
}

func (r *Spec) ApplyRule(in RecurrenceRule) error {
	switch in.Freq {
	case "daily", "weekly", "monthly", "yearly", "dates":
	default:
		return ErrInvalidRule
	}
	r.Freq = in.Freq
	r.Interval = in.Interval
	if r.Interval <= 0 {
		r.Interval = 1
	}
	r.Weekdays = nil
	for _, wd := range in.Weekdays {
		if wd < 1 || wd > 7 {
			return ErrInvalidRule
		}
		r.Weekdays = append(r.Weekdays, wd)
	}
	r.MonthlyMode, r.MonthDays, r.Months, r.MonthNth, r.MonthWeekday = nil, nil, nil, nil, nil
	if in.Freq == "monthly" || in.Freq == "yearly" {
		for _, d := range in.MonthDays {
			if d != LastDay && (d < 1 || d > 31) {
				return ErrInvalidRule
			}
			r.MonthDays = append(r.MonthDays, d)
		}
	}
	if in.Freq == "yearly" {
		for _, m := range in.Months {
			if m < 1 || m > 12 {
				return ErrInvalidRule
			}
			r.Months = append(r.Months, m)
		}
	}
	if in.Freq == "monthly" && in.Monthly != nil {
		m := *in.Monthly
		switch m.Mode {
		case "day":
		case "weekday":
			if m.Nth == nil || m.Weekday == nil || *m.Weekday < 1 || *m.Weekday > 7 ||
				(*m.Nth != -1 && (*m.Nth < 1 || *m.Nth > 5)) {
				return ErrInvalidRule
			}
			r.MonthNth, r.MonthWeekday = m.Nth, m.Weekday
		default:
			return ErrInvalidRule
		}
		mode := m.Mode
		r.MonthlyMode = &mode
	}
	r.Dates = nil
	for _, s := range in.Dates {
		d, err := ParseDate(s)
		if err != nil {
			return ErrInvalidRule
		}
		r.Dates = append(r.Dates, d)
	}
	if in.Freq == "dates" && len(r.Dates) == 0 {
		return ErrInvalidRule
	}
	return nil
}

func (r *Spec) ApplyEnd(in *RecurrenceEnd) error {
	if in == nil {
		r.EndKind, r.EndDate, r.EndCount = "none", nil, nil
		return nil
	}
	switch in.Kind {
	case "none", "":
		r.EndKind, r.EndDate, r.EndCount = "none", nil, nil
	case "date":
		if in.Date == nil {
			return ErrInvalidRule
		}
		d, err := ParseDate(*in.Date)
		if err != nil {
			return ErrInvalidRule
		}
		r.EndKind, r.EndDate, r.EndCount = "date", &d, nil
	case "count":
		if in.Count == nil || *in.Count < 1 {
			return ErrInvalidRule
		}
		c := *in.Count
		r.EndKind, r.EndDate, r.EndCount = "count", nil, &c
	default:
		return ErrInvalidRule
	}
	return nil
}

func (r *Spec) APIRule() RecurrenceRule {
	out := RecurrenceRule{Freq: r.Freq, Interval: r.Interval, Weekdays: []int{}, MonthDays: []int{}, Months: []int{},
		Dates: []string{}}
	out.Weekdays = append(out.Weekdays, r.Weekdays...)
	if r.Freq == "weekly" && len(out.Weekdays) == 0 {
		out.Weekdays = []int{IsoWeekday(r.StartDate)}
	}
	if r.Freq == "monthly" {
		m := &Monthly{Mode: "day"}
		if r.MonthlyMode != nil {
			m.Mode = *r.MonthlyMode
		}
		if m.Mode == "weekday" {
			m.Nth, m.Weekday = r.MonthNth, r.MonthWeekday
		}
		out.Monthly = m
	}
	if (r.Freq == "monthly" && out.Monthly.Mode == "day") || r.Freq == "yearly" {
		out.MonthDays = append(out.MonthDays, r.MonthDays...)
		if len(out.MonthDays) == 0 {
			out.MonthDays = []int{r.StartDate.Day()}
		}
	}
	if r.Freq == "yearly" {
		out.Months = append(out.Months, r.Months...)
		if len(out.Months) == 0 {
			out.Months = []int{int(r.StartDate.Month())}
		}
	}
	for _, d := range r.Dates {
		out.Dates = append(out.Dates, d.Format(DateLayout))
	}
	return out
}

func (r *Spec) APIEnd() RecurrenceEnd {
	out := RecurrenceEnd{Kind: r.EndKind, Count: r.EndCount}
	if r.EndDate != nil {
		s := r.EndDate.Format(DateLayout)
		out.Date = &s
	}
	return out
}

// Build checks a rule, a start date and an end as /recurring does and
// returns the schedule, ready for Dates and APIRule / APIEnd.
func Build(rule RecurrenceRule, start string, end *RecurrenceEnd, today time.Time) (*Spec, error) {
	sd, err := ParseDate(start)
	if err != nil {
		return nil, ErrInvalidRule
	}
	s := &Spec{StartDate: sd}
	if err := s.ApplyRule(rule); err != nil {
		return nil, err
	}
	if err := s.ApplyEnd(end); err != nil {
		return nil, err
	}
	if err := s.Limits(today); err != nil {
		return nil, err
	}
	return s, nil
}

// Within — every occurrence from the start to until that is inside the end
// (a Spec from Build).
func (r *Spec) Within(until time.Time) []time.Time {
	if (r.EndKind == "date" || r.EndKind == "count") && r.GeneratedUntil != nil && r.GeneratedUntil.Before(until) {
		until = *r.GeneratedUntil
	}
	return r.Occurrences(until, 0)
}

func utcDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// dayDiff is the whole days from a to b (both UTC midnights).
func dayDiff(a, b time.Time) int { return int(b.Sub(a) / (24 * time.Hour)) }

// ceilDiv is a / b rounded up, for a >= 0 and b > 0.
func ceilDiv(a, b int) int { return (a + b - 1) / b }

// endCap is the last date a window can reach: to, cut at the end of the
// schedule (the end date, or the n-th occurrence of a count end).
func (r *Spec) endCap(to time.Time) time.Time {
	switch r.EndKind {
	case "date", "count":
		if g := r.GeneratedUntil; g != nil {
			if g.Before(to) {
				return *g
			}
			return to
		}
		if r.EndKind == "date" && r.EndDate != nil && r.EndDate.Before(to) {
			return *r.EndDate
		}
		if r.EndKind == "count" && r.EndCount != nil && *r.EndCount > 0 {
			occ := r.Occurrences(r.MaxEnd(), *r.EndCount)
			if len(occ) == *r.EndCount && occ[len(occ)-1].Before(to) {
				return occ[len(occ)-1]
			}
		}
	}
	return to
}

// Between — the occurrences d with from <= d <= to that are inside the end
// (a Spec from Build). It is Within(to) cut to the window, but it jumps to
// the first period that can hold from instead of walking from the start, so
// a rule that began years ago costs the same as a new one. A count end still
// counts from the start: the cut comes from GeneratedUntil. A weekday listed
// twice is one weekday.
func (r *Spec) Between(from, to time.Time) []time.Time {
	out := []time.Time{}
	lo, to := utcDay(from), utcDay(to)
	if r.StartDate.After(lo) {
		lo = r.StartDate
	}
	hi := r.endCap(to)
	if lo.After(hi) {
		return r.dropGaps(out)
	}
	step := r.Interval
	if step < 1 {
		step = 1
	}
	start := r.StartDate
	switch r.Freq {
	case "daily":
		for k := ceilDiv(dayDiff(start, lo), step); ; k++ {
			d := start.AddDate(0, 0, k*step)
			if d.After(hi) {
				break
			}
			out = append(out, d)
		}
	case "weekly":
		days := append([]int(nil), r.Weekdays...)
		if len(days) == 0 {
			days = []int{IsoWeekday(start)}
		}
		sort.Ints(days)
		weekStart := start.AddDate(0, 0, -(IsoWeekday(start) - 1))
		j := 0
		if skip := dayDiff(weekStart, lo) - 6; skip > 0 {
			j = ceilDiv(skip, 7*step)
		}
		for ; ; j++ {
			w := weekStart.AddDate(0, 0, 7*step*j)
			if w.After(hi) {
				break
			}
			for i, wd := range days {
				if i > 0 && wd == days[i-1] {
					continue
				}
				d := w.AddDate(0, 0, wd-1)
				if d.Before(lo) {
					continue
				}
				if d.After(hi) {
					return r.dropGaps(out)
				}
				out = append(out, d)
			}
		}
	case "monthly":
		months := (lo.Year()-start.Year())*12 + int(lo.Month()-start.Month())
		for k := ceilDiv(months, step) * step; ; k += step {
			first := time.Date(start.Year(), start.Month()+time.Month(k), 1, 0, 0, 0, 0, time.UTC)
			if first.After(hi) {
				break
			}
			for _, d := range r.monthDates(first.Year(), first.Month()) {
				if d.Before(lo) {
					continue
				}
				if d.After(hi) {
					return r.dropGaps(out)
				}
				out = append(out, d)
			}
		}
	case "yearly":
		for k := ceilDiv(lo.Year()-start.Year(), step) * step; ; k += step {
			y := start.Year() + k
			if time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).After(hi) {
				break
			}
			for _, d := range r.yearDates(y) {
				if d.Before(lo) {
					continue
				}
				if d.After(hi) {
					return r.dropGaps(out)
				}
				out = append(out, d)
			}
		}
	case "dates":
		ds := append([]time.Time(nil), r.Dates...)
		sort.Slice(ds, func(i, j int) bool { return ds[i].Before(ds[j]) })
		for i, d := range ds {
			if i > 0 && d.Equal(ds[i-1]) {
				continue
			}
			if d.Before(lo) {
				continue
			}
			if d.After(hi) {
				break
			}
			out = append(out, d)
		}
	}
	return r.dropGaps(out)
}
