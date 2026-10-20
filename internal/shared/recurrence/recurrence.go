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

type Monthly struct {
	Mode    string `json:"mode"`
	Day     *int   `json:"day"`
	Nth     *int   `json:"nth"`
	Weekday *int   `json:"weekday"`
}

type RecurrenceRule struct {
	Freq     string   `json:"freq"`
	Interval int      `json:"interval"`
	Weekdays []int    `json:"weekdays"`
	Monthly  *Monthly `json:"monthly"`
	Dates    []string `json:"dates"`
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
	MonthDay       *int
	MonthNth       *int
	MonthWeekday   *int
	Dates          []time.Time
	StartDate      time.Time
	EndKind        string
	EndDate        *time.Time
	EndCount       *int
	TotalCount     *int
	GeneratedUntil *time.Time
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
		for k := 0; len(out) < maxOccurrences; k += r.Interval {
			first := time.Date(start.Year(), start.Month()+time.Month(k), 1, 0, 0, 0, 0, time.UTC)
			if first.After(until) {
				break
			}
			var d time.Time
			if r.MonthlyMode != nil && *r.MonthlyMode == "weekday" && r.MonthNth != nil && r.MonthWeekday != nil {
				var ok bool
				if d, ok = nthWeekday(first.Year(), first.Month(), *r.MonthNth, *r.MonthWeekday); !ok {
					continue
				}
			} else {
				day := start.Day()
				if r.MonthDay != nil {
					day = *r.MonthDay
				}
				if ld := lastDay(first.Year(), first.Month()); day > ld {
					day = ld
				}
				d = time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
			}
			if !add(d) {
				break
			}
		}
	case "yearly":
		for k := 0; len(out) < maxOccurrences; k += r.Interval {
			y := start.Year() + k
			day := start.Day()
			if ld := lastDay(y, start.Month()); day > ld {
				day = ld
			}
			d := time.Date(y, start.Month(), day, 0, 0, 0, 0, time.UTC)
			if d.After(until) {
				break
			}
			if !add(d) {
				break
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

func (r *Spec) MaxEnd() time.Time { return r.StartDate.AddDate(2, 0, 0) }

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
	r.MonthlyMode, r.MonthDay, r.MonthNth, r.MonthWeekday = nil, nil, nil, nil
	if in.Freq == "monthly" && in.Monthly != nil {
		m := *in.Monthly
		switch m.Mode {
		case "day":
			if m.Day == nil || *m.Day < 1 || *m.Day > 31 {
				return ErrInvalidRule
			}
			r.MonthDay = m.Day
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
	out := RecurrenceRule{Freq: r.Freq, Interval: r.Interval, Weekdays: []int{}, Dates: []string{}}
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
		} else {
			day := r.StartDate.Day()
			if r.MonthDay != nil {
				day = *r.MonthDay
			}
			m.Day = &day
		}
		out.Monthly = m
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
		return out
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
					return out
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
			var d time.Time
			if r.MonthlyMode != nil && *r.MonthlyMode == "weekday" && r.MonthNth != nil && r.MonthWeekday != nil {
				var ok bool
				if d, ok = nthWeekday(first.Year(), first.Month(), *r.MonthNth, *r.MonthWeekday); !ok {
					continue
				}
			} else {
				day := start.Day()
				if r.MonthDay != nil {
					day = *r.MonthDay
				}
				if ld := lastDay(first.Year(), first.Month()); day > ld {
					day = ld
				}
				d = time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
			}
			if d.Before(lo) {
				continue
			}
			if d.After(hi) {
				break
			}
			out = append(out, d)
		}
	case "yearly":
		for k := ceilDiv(lo.Year()-start.Year(), step) * step; ; k += step {
			y := start.Year() + k
			day := start.Day()
			if ld := lastDay(y, start.Month()); day > ld {
				day = ld
			}
			d := time.Date(y, start.Month(), day, 0, 0, 0, 0, time.UTC)
			if d.After(hi) {
				break
			}
			if d.Before(lo) {
				continue
			}
			out = append(out, d)
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
	return out
}
