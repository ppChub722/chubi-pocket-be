package saving_goals

import (
	"math"
	"time"
)

func lastDay(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// periodStart is the first day of the k-th period (k ≥ 0) counted from anchor, every
// interval freq; a month day past a short month's end lands on its last day (as budgets).
func periodStart(freq string, interval int, anchor time.Time, k int) time.Time {
	n := k * interval
	switch freq {
	case "daily":
		return anchor.AddDate(0, 0, n)
	case "weekly":
		return anchor.AddDate(0, 0, 7*n)
	case "yearly":
		y := anchor.Year() + n
		d := anchor.Day()
		if ld := lastDay(y, anchor.Month()); d > ld {
			d = ld
		}
		return time.Date(y, anchor.Month(), d, 0, 0, 0, 0, time.UTC)
	}
	first := time.Date(anchor.Year(), anchor.Month()+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	d := anchor.Day()
	if ld := lastDay(first.Year(), first.Month()); d > ld {
		d = ld
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, time.UTC)
}

// periodAt is the period holding day: its index, first and last day. A day before the
// anchor is in the first period.
func periodAt(freq string, interval int, anchor, day time.Time) (int, time.Time, time.Time) {
	if interval < 1 {
		interval = 1
	}
	k := 0
	if day.After(anchor) {
		switch freq {
		case "daily":
			k = days(anchor, day) / interval
		case "weekly":
			k = days(anchor, day) / (7 * interval)
		case "yearly":
			k = (day.Year() - anchor.Year()) / interval
		default:
			k = monthsBetween(anchor, day) / interval
		}
		for k > 0 && periodStart(freq, interval, anchor, k).After(day) {
			k--
		}
		for !periodStart(freq, interval, anchor, k+1).After(day) {
			k++
		}
	}
	return k, periodStart(freq, interval, anchor, k), periodStart(freq, interval, anchor, k+1).AddDate(0, 0, -1)
}

// periodView is a goal's เป้าต่อรอบ (nil = none) and, for an active goal with a target
// that isn't reached, the period today is in. daily is the goal's net per day
// ("YYYY-MM-DD"); plan answers what its saving settings put in between two days.
func periodView(g *Goal, saved float64, daily map[string]float64, plan func(from, to time.Time) *float64, today time.Time) *GoalPeriod {
	if g.PeriodAmount == nil || g.PeriodFreq == nil || g.PeriodAnchor == nil {
		return nil
	}
	freq, interval, anchor, amount := *g.PeriodFreq, g.PeriodInterval, *g.PeriodAnchor, *g.PeriodAmount
	out := &GoalPeriod{Amount: round2(amount), Freq: freq, Interval: interval, Anchor: anchor.Format(dateLayout)}
	if g.Status != StateActive || g.TargetAmount == nil || saved >= *g.TargetAmount-0.004 {
		return out
	}
	k, start, end := periodAt(freq, interval, anchor, today)
	var in float64
	for d, v := range daily {
		if t, err := time.Parse(dateLayout, d); err == nil && !t.Before(start) && !t.After(end) {
			in += v
		}
	}
	left := *g.TargetAmount - saved
	// What the goal still needed when the period began caps its target.
	target := math.Min(amount, left+in)
	short := math.Max(0, target-in)
	// This period brings short more; then every period one amount (no carry-over).
	n := 0
	if rest := left - short; rest > 0.004 {
		n = int(math.Ceil(rest/amount - 1e-9))
	}
	eta := periodStart(freq, interval, anchor, k+n+1).AddDate(0, 0, -1)
	out.Current = &PeriodNow{
		Start: start.Format(dateLayout), End: end.Format(dateLayout),
		Target: round2(target), Saved: round2(in), Short: round2(short), DaysLeft: days(today, end),
		EtaDate: eta.Format(dateLayout), Late: g.Deadline != nil && eta.After(*g.Deadline),
		Plan: plan(start, end),
	}
	return out
}
