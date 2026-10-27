package recurring

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// 0.4.1-1 R7: at most 3 due occurrences; a 4th turns the setting inactive.
func TestOverdueLimit(t *testing.T) {
	f := newRecFix(t)
	var d Detail
	f.call("POST", "/recurring", "createRecurring", map[string]any{"name": "Gym", "type": "expense", "amount": 10,
		"account_id": f.wallet, "rule": map[string]any{"freq": "daily", "interval": 1}, "start_date": f.day(-5)}, 201, &d)
	id := d.Recurring.ID
	f.track(id) // 6 due: -5 … today
	if err := f.svc.DueCheck(f.ctx, f.me, ""); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, &d)
	if d.Recurring.Status != "paused" || av2(d.Recurring.PauseReason) != PauseOverdueLimit || d.Recurring.Counts.Due != 3 ||
		d.Recurring.Counts.Upcoming != 0 || f.storedRows(id) != 3 {
		t.Fatalf("after the check: status %s reason %v counts %+v rows %d",
			d.Recurring.Status, d.Recurring.PauseReason, d.Recurring.Counts, f.storedRows(id))
	}
	var due ItemList
	f.call("GET", "/recurring-items?status=due&rule_id="+id.String(), "listRecurringItems", nil, 200, &due)
	if len(due.Data) != 3 || due.Data[0].Recurring == nil || av2(due.Data[0].Recurring.PauseReason) != PauseOverdueLimit {
		t.Fatalf("due while inactive = %+v", due.Data)
	}
	slots := map[string]bool{}
	for _, it := range due.Data {
		slots[it.Slot] = true
	}
	if !slots[f.day(-5)] || !slots[f.day(-4)] || !slots[f.day(-3)] {
		t.Errorf("the oldest 3 stay due: %v", slots)
	}

	// turning it on without clearing them: today's occurrence is the 4th → inactive again at once
	f.call("POST", "/recurring/"+id.String()+"/resume", "resumeRecurring", nil, 200, &d)
	if d.Recurring.Status != "paused" || av2(d.Recurring.PauseReason) != PauseOverdueLimit || d.Recurring.Counts.Due != 3 {
		t.Fatalf("resume with 3 waiting: status %s counts %+v", d.Recurring.Status, d.Recurring.Counts)
	}
	// clearing them does not resume it; resuming after does: only today's is due, the gap is never made
	var b BulkResult
	f.call("POST", "/recurring/"+id.String()+"/occurrences/skip-ready", "skipRecurringOccurrencesReady", nil, 200, &b)
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, &d)
	if len(b.Items) != 3 || d.Recurring.Status != "paused" {
		t.Fatalf("skip-ready while inactive: %d items, status %s", len(b.Items), d.Recurring.Status)
	}
	f.call("POST", "/recurring/"+id.String()+"/resume", "resumeRecurring", nil, 200, &d)
	if d.Recurring.Status != "active" || d.Recurring.PauseReason != nil || d.Recurring.Counts.Due != 1 || d.Recurring.Counts.Skipped != 3 {
		t.Fatalf("resume after clearing: status %s counts %+v", d.Recurring.Status, d.Recurring.Counts)
	}

	// auto-create settings are never limited: every missed one is recorded
	var a Detail
	f.call("POST", "/recurring", "createRecurring", map[string]any{"name": "Auto", "type": "expense", "amount": 1,
		"account_id": f.wallet, "rule": map[string]any{"freq": "daily", "interval": 1}, "start_date": f.day(-5), "auto_create": true}, 201, &a)
	f.track(a.Recurring.ID)
	f.exec(`UPDATE recurring_rules SET auto_created_until = $2 WHERE id = $1`, a.Recurring.ID, f.today.AddDate(0, 0, -6))
	if err := f.svc.DueCheck(f.ctx, f.me, ""); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/recurring/"+a.Recurring.ID.String(), "getRecurring", nil, 200, &a)
	if a.Recurring.Status != "active" || a.Recurring.Counts.Done != 6 || a.Recurring.Counts.Due != 0 {
		t.Errorf("auto-create catch-up: status %s counts %+v", a.Recurring.Status, a.Recurring.Counts)
	}
}

// 0.4.1-1 R8 / R9: resuming starts at the current period; an installment ending by count
// shifts its numbers and its end, one ending by date keeps its date.
func TestResumeShiftsInstallments(t *testing.T) {
	f := newRecFix(t)
	first := time.Date(f.today.Year(), f.today.Month(), 1, 0, 0, 0, 0, time.UTC)
	month := func(n int) string { return first.AddDate(0, n, 0).Format(dateLayout) }
	var d Detail
	f.call("POST", "/recurring", "createRecurring", map[string]any{"name": "Phone", "kind": "installment", "amount": 1000,
		"account_id": f.wallet, "rule": map[string]any{"freq": "monthly", "interval": 1}, "start_date": month(-6),
		"end": map[string]any{"kind": "count", "count": 10}}, 201, &d)
	id := d.Recurring.ID
	f.track(id)
	for _, n := range []int{-6, -5, -4} { // no. 1–3 paid
		f.call("POST", f.occ(id, month(n))+"/mark-paid", "markRecurringOccurrencePaid", nil, 200, nil)
	}
	// out of use from no. 4 on (paused at the 4th month's slot, as the limit would)
	f.exec(`UPDATE recurring_rules SET status = 'paused', pause_reason = 'overdue_limit', paused_from = $2 WHERE id = $1`,
		id, first.AddDate(0, -3, 0))
	f.call("POST", "/recurring/"+id.String()+"/resume", "resumeRecurring", nil, 200, &d)
	if d.Recurring.Status != "active" || d.Recurring.Counts.Due != 1 || d.Recurring.Counts.Paid != 3 ||
		d.Recurring.Progress.Total == nil || *d.Recurring.Progress.Total != 10 {
		t.Fatalf("after resume: status %s counts %+v progress %+v", d.Recurring.Status, d.Recurring.Counts, d.Recurring.Progress)
	}
	if it := f.slotItem(id, month(0)); it.Status != StatusDue || it.Seq == nil || *it.Seq != 4 {
		t.Errorf("this month's = %+v, want no. 4 due", it)
	}
	up := f.items(id, "upcoming")
	if len(up) != 6 || up[len(up)-1].Slot != month(6) || up[len(up)-1].Seq == nil || *up[len(up)-1].Seq != 10 {
		t.Fatalf("upcoming = %d, last %+v (want no. 10 on %s)", len(up), up[len(up)-1], month(6))
	}
	if _, err := f.svc.loadOcc(f.ctx, f.pool, f.me, mustRule(t, f, id), mustDate(t, month(-2)), f.today); err == nil {
		t.Errorf("an occurrence of the gap (%s) still exists", month(-2))
	}

	// an end by date keeps its date: the gap only drops occurrences
	var e Detail
	f.call("POST", "/recurring", "createRecurring", map[string]any{"name": "Gym pass", "type": "expense", "amount": 500,
		"account_id": f.wallet, "rule": map[string]any{"freq": "monthly", "interval": 1}, "start_date": month(-6),
		"end": map[string]any{"kind": "date", "date": month(6)}}, 201, &e)
	f.track(e.Recurring.ID)
	f.exec(`UPDATE recurring_rules SET status = 'paused', pause_reason = 'manual', paused_from = $2 WHERE id = $1`,
		e.Recurring.ID, first.AddDate(0, -6, 0))
	f.call("POST", "/recurring/"+e.Recurring.ID.String()+"/resume", "resumeRecurring", nil, 200, &e)
	if e.Recurring.End.Date == nil || *e.Recurring.End.Date != month(6) || e.Recurring.Progress.Total == nil || *e.Recurring.Progress.Total != 7 {
		t.Errorf("date end: end %+v progress %+v (want %s, 7 left)", e.Recurring.End, e.Recurring.Progress, month(6))
	}
}

func mustRule(t *testing.T, f *recFix, id uuid.UUID) *Rule {
	t.Helper()
	r, err := getRule(f.ctx, f.pool, f.me, id, false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// 0.4.1-1 R3: whatever deletes the transaction (an event bill, a debt, recurring itself),
// the occurrence it fills is skipped, never left pointing nowhere or quietly due.
func TestTxDeleteFromAnyPathSkips(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6)
	id := d.Recurring.ID
	f.track(id)
	var w ItemWriteResult
	f.call("POST", f.occ(id, f.day(-14))+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 200, &w)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.txs.DeleteInTx(f.ctx, tx, f.me, w.Item.Transaction.ID); err != nil { // the event / debt path
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if it := f.slotItem(id, f.day(-14)); it.Status != StatusSkipped || it.Transaction != nil {
		t.Errorf("after DeleteInTx: %+v", it)
	}

	f.call("POST", f.occ(id, f.day(-7))+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 200, &w)
	tx, _ = f.pool.Begin(f.ctx)
	if _, err := f.txs.DeleteDebtTxInTx(f.ctx, tx, f.me, w.Item.Transaction.ID); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit(f.ctx)
	if it := f.slotItem(id, f.day(-7)); it.Status != StatusSkipped {
		t.Errorf("after DeleteDebtTxInTx: %+v", it)
	}

	// recurring's own delete of an item with its tx removes the row, nothing is left skipped
	f.call("POST", f.occ(id, f.day(0))+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 200, &w)
	f.call("DELETE", "/recurring-items/"+w.Item.ID.String()+"?delete_linked_tx=true", "", nil, 200, nil)
	if it := f.slotItem(id, f.day(0)); it.Status != StatusDue || it.Stored {
		t.Errorf("after deleting the item with its tx: %+v", it)
	}
}
