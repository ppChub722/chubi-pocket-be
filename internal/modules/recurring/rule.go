package recurring

// Recurrence (Google Calendar style) and its limits (owner 2026-10-13):
// a set end ≤ 2 years after the start; no end = a bounded horizon (daily /
// weekly 3 months, monthly / yearly 1 year) extended by "ต่ออีก".

import (
	"errors"
	"sort"
	"time"
)

var (
	// ErrInvalidRule — the rule can't produce dates (400 INVALID_RULE).
	ErrInvalidRule = errors.New("invalid recurrence rule")
	// ErrEndTooFar — the end is more than 2 years after the start.
	ErrEndTooFar = errors.New("the end must be at most 2 years after the start")
)

const maxOccurrences = 1000 // safety stop for any loop

func parseDate(s string) (time.Time, error) {
	return time.Parse(dateLayout, s)
}

func lastDay(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// isoWeekday — 1 = Monday … 7 = Sunday.
func isoWeekday(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

// nthWeekday — the nth (1..5, -1 = last) ISO weekday of y/m; ok=false when
// the month has no such day.
func nthWeekday(y int, m time.Month, nth, weekday int) (time.Time, bool) {
	if nth == -1 {
		d := time.Date(y, m, lastDay(y, m), 0, 0, 0, 0, time.UTC)
		for isoWeekday(d) != weekday {
			d = d.AddDate(0, 0, -1)
		}
		return d, true
	}
	d := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	for isoWeekday(d) != weekday {
		d = d.AddDate(0, 0, 1)
	}
	d = d.AddDate(0, 0, 7*(nth-1))
	return d, d.Month() == m
}

// occurrences — the rule's dates from its start, up to `until` (inclusive)
// and at most `limit` of them (0 = no count limit). Sorted; seq = index+1.
func (r *Rule) occurrences(until time.Time, limit int) []time.Time {
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
			days = []int{isoWeekday(start)}
		}
		sort.Ints(days)
		weekStart := start.AddDate(0, 0, -(isoWeekday(start) - 1))
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

// horizonSpan — how far a no-end setting is written ahead.
func (r *Rule) horizonSpan(from time.Time) time.Time {
	switch r.Freq {
	case "daily", "weekly":
		return from.AddDate(0, 3, 0)
	}
	return from.AddDate(1, 0, 0)
}

// maxEnd — the latest date a set end may reach.
func (r *Rule) maxEnd() time.Time { return r.StartDate.AddDate(2, 0, 0) }

// limits computes total_count and generated_until for the rule, from today
// (the horizon of a no-end rule starts at max(today, start)).
func (r *Rule) limits(today time.Time) error {
	switch r.Freq {
	case "dates":
		occ := r.occurrences(r.maxEnd(), 0)
		if len(occ) == 0 {
			return ErrInvalidRule
		}
		for _, d := range r.Dates {
			if d.After(r.maxEnd()) || d.Before(r.StartDate) {
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
		if r.EndDate.After(r.maxEnd()) {
			return ErrEndTooFar
		}
		n := len(r.occurrences(*r.EndDate, 0))
		if n == 0 {
			return ErrInvalidRule
		}
		end := *r.EndDate
		r.TotalCount, r.GeneratedUntil, r.EndCount = &n, &end, nil
	case "count":
		if r.EndCount == nil || *r.EndCount < 1 {
			return ErrInvalidRule
		}
		occ := r.occurrences(r.maxEnd(), *r.EndCount)
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
		h := r.horizonSpan(from)
		if r.GeneratedUntil != nil && r.GeneratedUntil.After(h) {
			h = *r.GeneratedUntil
		}
		r.GeneratedUntil = &h
	}
	return nil
}

// applyRule copies an API rule onto the row (validated).
func (r *Rule) applyRule(in RecurrenceRule) error {
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
		d, err := parseDate(s)
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

// applyEnd copies an API end onto the row.
func (r *Rule) applyEnd(in *RecurrenceEnd) error {
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
		d, err := parseDate(*in.Date)
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

// apiRule / apiEnd — the row as the spec shapes.
func (r *Rule) apiRule() RecurrenceRule {
	out := RecurrenceRule{Freq: r.Freq, Interval: r.Interval, Weekdays: []int{}, Dates: []string{}}
	out.Weekdays = append(out.Weekdays, r.Weekdays...)
	if r.Freq == "weekly" && len(out.Weekdays) == 0 {
		out.Weekdays = []int{isoWeekday(r.StartDate)}
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
		out.Dates = append(out.Dates, d.Format(dateLayout))
	}
	return out
}

func (r *Rule) apiEnd() RecurrenceEnd {
	out := RecurrenceEnd{Kind: r.EndKind, Count: r.EndCount}
	if r.EndDate != nil {
		s := r.EndDate.Format(dateLayout)
		out.Date = &s
	}
	return out
}
