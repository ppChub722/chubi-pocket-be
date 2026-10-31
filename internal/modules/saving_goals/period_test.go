package saving_goals

import (
	"net/http"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, _ := time.Parse(dateLayout, s)
	return t
}

func TestPeriodAt(t *testing.T) {
	for _, c := range []struct {
		name, freq         string
		interval           int
		anchor, at         string
		k                  int
		wantStart, wantEnd string
	}{
		{"calendar month", "monthly", 1, "2026-01-01", "2026-10-26", 9, "2026-10-01", "2026-10-31"},
		{"every 2 months", "monthly", 2, "2026-11-01", "2027-02-10", 1, "2027-01-01", "2027-02-28"},
		{"month from the 15th", "monthly", 1, "2026-10-15", "2026-11-03", 0, "2026-10-15", "2026-11-14"},
		{"the 31st in a short month", "monthly", 1, "2026-01-31", "2026-02-28", 1, "2026-02-28", "2026-03-30"},
		{"weekly", "weekly", 1, "2026-10-05", "2026-10-26", 3, "2026-10-26", "2026-11-01"},
		{"every 3 days", "daily", 3, "2026-10-01", "2026-10-08", 2, "2026-10-07", "2026-10-09"},
		{"yearly", "yearly", 1, "2026-04-01", "2027-03-31", 0, "2026-04-01", "2027-03-31"},
		{"before the anchor", "monthly", 1, "2026-11-01", "2026-10-20", 0, "2026-11-01", "2026-11-30"},
	} {
		k, s, e := periodAt(c.freq, c.interval, day(c.anchor), day(c.at))
		if k != c.k || s.Format(dateLayout) != c.wantStart || e.Format(dateLayout) != c.wantEnd {
			t.Errorf("%s: %d %s–%s, want %d %s–%s", c.name, k, s.Format(dateLayout), e.Format(dateLayout),
				c.k, c.wantStart, c.wantEnd)
		}
	}
}

// The owner's example: ฿50,000 by the end of March, ฿30,000 every 2 months from 1 Nov.
// Period 1 got ฿4,000; period 2 (today) has ฿8,000 so far.
func TestPeriodView(t *testing.T) {
	target, amount, freq, anchor, deadline := 50000.0, 30000.0, "monthly", day("2026-11-01"), day("2027-03-31")
	g := &Goal{Status: StateActive, TargetAmount: &target, Deadline: &deadline, PeriodAmount: &amount, PeriodFreq: &freq,
		PeriodInterval: 2, PeriodAnchor: &anchor}
	daily := map[string]float64{"2026-11-25": 2000, "2026-12-25": 2000, "2027-01-10": 8000}
	none := func(time.Time, time.Time) *float64 { return nil }
	p := periodView(g, 12000, daily, none, day("2027-01-20"))
	c := p.Current
	if c == nil || c.Start != "2027-01-01" || c.End != "2027-02-28" || c.Target != 30000 || c.Saved != 8000 || c.Short != 22000 {
		t.Fatalf("current = %+v", c)
	}
	// 38,000 left: 22,000 more now, then 16,000 in Mar–Apr → done end of April, after the deadline.
	if c.EtaDate != "2027-04-30" || !c.Late || c.DaysLeft != 39 {
		t.Errorf("eta %s late %v days %d", c.EtaDate, c.Late, c.DaysLeft)
	}

	// The last period's target is what the goal still needs.
	p = periodView(g, 40000, map[string]float64{"2027-01-05": 0}, none, day("2027-01-20"))
	if p.Current.Target != 10000 || p.Current.Short != 10000 || p.Current.EtaDate != "2027-02-28" || p.Current.Late {
		t.Errorf("last period = %+v", p.Current)
	}

	// Saving more than the period's target brings the end nearer; the next target stays.
	p = periodView(g, 39000, map[string]float64{"2027-01-10": 35000}, none, day("2027-01-20"))
	if p.Current.Target != 30000 || p.Current.Short != 0 || p.Current.EtaDate != "2027-04-30" {
		t.Errorf("over = %+v", p.Current)
	}

	// Reached: the setting, no current period.
	if p = periodView(g, 50000, nil, none, day("2027-01-20")); p == nil || p.Current != nil || p.Amount != 30000 {
		t.Errorf("reached = %+v", p)
	}
}

func TestGoalPeriodAPI(t *testing.T) {
	f := newGoalFix(t)
	dl := f.today.AddDate(0, 6, 0).Format(dateLayout)
	goal := f.goal("Car", 50000, &dl)
	f.save(f.kbank, &goal, 4000)
	monthStart := time.Date(f.today.Year(), f.today.Month(), 1, 0, 0, 0, 0, time.UTC)
	f.savingRule(&goal, 2000, 31, 0, monthStart.Format(dateLayout))

	var d GoalDetail
	f.call("PUT", "/saving-goals/"+goal.String(), "updateSavingGoal", map[string]any{
		"period_amount": 30000,
		"period":        map[string]any{"freq": "monthly", "interval": 1, "anchor": monthStart.Format(dateLayout)},
	}, http.StatusOK, &d)
	p := d.Goal.Period
	if p == nil || p.Amount != 30000 || p.Freq != "monthly" || p.Interval != 1 || p.Current == nil {
		t.Fatalf("period = %+v", p)
	}
	if c := p.Current; c.Start != monthStart.Format(dateLayout) || c.Target != 30000 || c.Saved != 4000 || c.Short != 26000 ||
		c.Plan == nil || *c.Plan != 2000 {
		t.Errorf("current = %+v", c)
	}

	// A period amount alone defaults to calendar months.
	other := f.goal("Bike", 9000, nil)
	f.call("PUT", "/saving-goals/"+other.String(), "updateSavingGoal", map[string]any{"period_amount": 3000},
		http.StatusOK, &d)
	if p := d.Goal.Period; p == nil || p.Freq != "monthly" || p.Interval != 1 || p.Anchor[8:] != "01" {
		t.Errorf("default period = %+v", p)
	}

	// null clears it.
	f.call("PUT", "/saving-goals/"+goal.String(), "updateSavingGoal", map[string]any{"period_amount": nil},
		http.StatusOK, &d)
	if d.Goal.Period != nil {
		t.Errorf("cleared = %+v", d.Goal.Period)
	}
	raw := f.call("PUT", "/saving-goals/"+goal.String(), "", map[string]any{
		"period_amount": 1000, "period": map[string]any{"freq": "hourly", "interval": 1, "anchor": "2026-01-01"},
	}, http.StatusBadRequest, nil)
	if errCode(raw) != "VALIDATION_ERROR" {
		t.Errorf("bad freq = %s", raw)
	}
}

// A recurring saving never takes the goal past its target; at the target it is refused.
func TestRecurringSavingCappedAtTarget(t *testing.T) {
	f := newGoalFix(t)
	goal := f.goal("Phone", 10000, nil)
	f.save(f.kbank, &goal, 9000)
	svc := NewService(NewStore(f.pool))
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	id, err := svc.SaveFromDraftTx(f.ctx, tx, f.me, &f.kbank, &goal, 3000, f.today.Format(dateLayout), nil, SourceRecurring)
	if err != nil {
		t.Fatal(err)
	}
	var amount float64
	if err := tx.QueryRow(f.ctx, `SELECT amount::float8 FROM saving_entries WHERE id = $1`, id).Scan(&amount); err != nil || amount != 1000 {
		t.Fatalf("capped amount = %v (%v), want 1000", amount, err)
	}
	if _, err := svc.SaveFromDraftTx(f.ctx, tx, f.me, &f.kbank, &goal, 3000, f.today.Format(dateLayout), nil,
		SourceRecurring); err != ErrGoalReached {
		t.Errorf("at the target = %v, want ErrGoalReached", err)
	}
}

func TestAddMonthsClampsToMonthEnd(t *testing.T) {
	cases := []struct {
		from string
		n    int
		want string
	}{
		{"2026-10-31", 8, "2027-06-30"},
		{"2026-01-31", 1, "2026-02-28"},
		{"2028-01-31", 1, "2028-02-29"},
		{"2026-10-15", 3, "2027-01-15"},
		{"2026-12-31", 2, "2027-02-28"},
	}
	for _, c := range cases {
		if got := addMonths(day(c.from), c.n).Format(dateLayout); got != c.want {
			t.Errorf("addMonths(%s, %d) = %s, want %s", c.from, c.n, got, c.want)
		}
	}
}
