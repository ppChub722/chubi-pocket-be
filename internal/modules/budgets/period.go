package budgets

import "time"

const maxPeriods = 5000 // safety stop for any loop

// span — one period's dates (end nil = open-ended).
type span struct {
	Start time.Time
	End   *time.Time
}

func lastDay(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func days(a, b time.Time) int { return int(b.Sub(a).Hours() / 24) }

func dptr(t time.Time) *time.Time { return &t }

// nthStart — the start of the k-th period counted from anchor.
func (b *Budget) nthStart(anchor time.Time, k int) time.Time {
	n := k * b.Interval
	switch *b.Freq {
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

// stopDate — the last day the budget may count, from its end and ปิดงบ
// (nil = open). baseSeq = periods kept before the anchor (a count end
// counts them too).
func (b *Budget) stopDate(baseSeq int) *time.Time {
	var stop *time.Time
	min := func(t time.Time) {
		if stop == nil || t.Before(*stop) {
			stop = dptr(t)
		}
	}
	if b.EndKind == "date" && b.EndDate != nil {
		min(*b.EndDate)
	}
	if b.Recurring && b.EndKind == "count" && b.EndCount != nil {
		left := *b.EndCount - baseSeq
		if left <= 0 {
			min(b.AnchorDate.AddDate(0, 0, -1))
		} else {
			min(b.nthStart(b.AnchorDate, left).AddDate(0, 0, -1))
		}
	}
	if b.ClosedOn != nil {
		min(*b.ClosedOn)
	}
	return stop
}

// spans — the periods from the anchor that have started by `until` (the
// first one always), each cut at the stop date.
func (b *Budget) spans(baseSeq int, until time.Time) []span {
	stop := b.stopDate(baseSeq)
	cut := func(sp span) span {
		if stop != nil && (sp.End == nil || stop.Before(*sp.End)) {
			sp.End = dptr(*stop)
		}
		return sp
	}
	if !b.Recurring {
		sp := span{Start: b.StartDate}
		if b.EndKind == "date" && b.EndDate != nil {
			sp.End = dptr(*b.EndDate)
		}
		return []span{cut(sp)}
	}
	var out []span
	for k := 0; k < maxPeriods; k++ {
		s := b.nthStart(b.AnchorDate, k)
		if k > 0 && (s.After(until) || (stop != nil && s.After(*stop))) {
			break
		}
		out = append(out, cut(span{Start: s, End: dptr(b.nthStart(b.AnchorDate, k+1).AddDate(0, 0, -1))}))
	}
	return out
}

// state — the budget's state on `today`.
func (b *Budget) state(today time.Time, baseSeq int) string {
	switch {
	case b.Status == StatusArchived:
		return StateArchived
	case b.ClosedOn != nil:
		return StateEnded
	case b.StartDate.After(today):
		return StateUpcoming
	}
	if stop := b.stopDate(baseSeq); stop != nil && stop.Before(today) {
		return StateEnded
	}
	return StateActive
}

func (b *Budget) apiRule() *Rule {
	if !b.Recurring || b.Freq == nil {
		return nil
	}
	return &Rule{Freq: *b.Freq, Interval: b.Interval}
}

func (b *Budget) apiEnd() End {
	out := End{Kind: b.EndKind, Count: b.EndCount}
	if b.EndDate != nil {
		s := b.EndDate.Format(dateLayout)
		out.Date = &s
	}
	return out
}
