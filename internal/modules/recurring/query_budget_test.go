package recurring

import (
	"context"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/database"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// The recurring reads must stay a fixed number of queries however many settings there
// are (the budget excludes the auth middleware's one user lookup).
func TestRecurringReadsQueryBudget(t *testing.T) {
	f := newRecFix(t)
	var first Detail
	for i := 0; i < 4; i++ {
		d := f.createWeekly(6)
		f.track(d.Recurring.ID)
		if i == 0 {
			first = d
		}
	}
	id := first.Recurring.ID.String()
	month := f.today.Format("2006-01")
	for _, c := range []struct {
		name, path string
		max        int64
	}{
		{"list", "/recurring?status=all&page=1&per_page=100", 11},
		{"get", "/recurring/" + id, 12},
		{"rule items", "/recurring/" + id + "/items?tab=produced&page=1", 5},
		{"items of the month", "/recurring-items?month=" + month + "&status=all&page=1&per_page=100", 5},
		{"items due", "/recurring-items?status=due&page=1&per_page=100", 6},
		{"occurrences", "/recurring/" + id + "/occurrences", 5},
	} {
		status, raw, n := apitest.DoCounted(t, f.r, "GET", "/api/v1"+c.path, nil)
		if status != 200 {
			t.Fatalf("%s: status %d: %s", c.name, status, raw)
		}
		t.Logf("%s: %d queries", c.name, n)
		if n > c.max {
			t.Errorf("%s ran %d queries, budget %d", c.name, n, c.max)
		}
	}
}

// A GET computes `due` from what is stored; it never writes, so the day's check stays
// for POST /sync and the recurring writes.
func TestRecurringReadsNeverWrite(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6)
	id := d.Recurring.ID
	f.track(id)
	f.exec(`UPDATE recurring_rules SET due_notified_until = $2 WHERE id = $1`, id, f.today.AddDate(0, 0, -30))
	f.exec(`DELETE FROM recurring_due_checks WHERE user_id = $1`, f.me)
	f.exec(`DELETE FROM notifications WHERE recipient_user_id = $1`, f.me)
	var before string
	_ = f.pool.QueryRow(f.ctx, `SELECT updated_at::text FROM recurring_rules WHERE id = $1`, id).Scan(&before)

	for _, p := range []string{
		"/recurring", "/recurring/" + id.String(), "/recurring/" + id.String() + "/items?tab=produced",
		"/recurring-items?status=due", "/recurring/" + id.String() + "/occurrences",
	} {
		f.call("GET", p, "", nil, 200, nil)
	}

	var after string
	var due *string
	var checks int
	_ = f.pool.QueryRow(f.ctx, `SELECT updated_at::text, due_notified_until::text FROM recurring_rules WHERE id = $1`, id).Scan(&after, &due)
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM recurring_due_checks WHERE user_id = $1`, f.me).Scan(&checks)
	if after != before || checks != 0 {
		t.Errorf("a GET wrote: updated_at %s -> %s, due_checks rows %d", before, after, checks)
	}
	if rows, _ := f.notices("recurring_due"); rows != 0 {
		t.Errorf("a GET made a recurring_due notice (rows %d)", rows)
	}
}

func TestDueCheckDailyRunsOncePerLocalDay(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6) // a write: its due check counts as today's
	id := d.Recurring.ID

	ctx, stats := database.WithQueryStats(shared.WithUserPrefs(context.Background()))
	if err := f.svc.DueCheckDaily(ctx, f.me, ""); err != nil {
		t.Fatal(err)
	}
	if n := stats.Count(); n > 2 {
		t.Errorf("a checked day cost %d queries, want at most 2 (preferences + guard)", n)
	}

	// a wallet archived since: the next sync's check pauses its settings
	f.exec(`UPDATE accounts SET status = 'archived' WHERE id = $1`, f.wallet)
	if err := f.svc.DueCheckDaily(f.ctx, f.me, ""); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = f.pool.QueryRow(f.ctx, `SELECT status FROM recurring_rules WHERE id = $1`, id).Scan(&status)
	if status != "paused" {
		t.Errorf("a setting on an archived wallet is %q after the check, want paused", status)
	}

	// a new day: the guard lets it through again
	f.exec(`UPDATE recurring_due_checks SET checked_on = checked_on - 1 WHERE user_id = $1`, f.me)
	ctx2, stats2 := database.WithQueryStats(shared.WithUserPrefs(context.Background()))
	if err := f.svc.DueCheckDaily(ctx2, f.me, ""); err != nil {
		t.Fatal(err)
	}
	var on string
	_ = f.pool.QueryRow(f.ctx, `SELECT checked_on::text FROM recurring_due_checks WHERE user_id = $1`, f.me).Scan(&on)
	if on != f.today.Format(dateLayout) || stats2.Count() <= 2 {
		t.Errorf("yesterday's check did not run again: checked_on %s, %d queries", on, stats2.Count())
	}
}
