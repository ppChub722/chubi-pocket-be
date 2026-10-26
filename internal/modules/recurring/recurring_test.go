package recurring

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/tags"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type recFix struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	me      uuid.UUID
	wallet  uuid.UUID
	food    uuid.UUID
	today   time.Time
	svc     *Service
	pending *pending.Service
	txs     *transactions.Service
	savings *saving_goals.Service
	r       http.Handler
}

func newRecFix(t *testing.T) *recFix {
	t.Helper()
	pool := testdb.StatsPool(t)
	ctx := context.Background()
	f := &recFix{t: t, ctx: ctx, pool: pool}
	f.me = testdb.User(t, pool)
	f.wallet = testdb.Account(t, pool, f.me)
	f.exec(`UPDATE accounts SET balance = 1000 WHERE id = $1`, f.wallet)
	f.food = testdb.Category(t, pool, f.me, "Food")
	loc, _ := time.LoadLocation("Asia/Bangkok")
	now := time.Now().In(loc)
	f.today = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	cats := categories.NewService(categories.NewStore(pool))
	f.txs = transactions.NewService(transactions.NewStore(pool), cats)
	f.pending = pending.NewService(pending.NewStore(pool), f.txs, tags.NewService(tags.NewStore(pool)), nil, nil)
	f.svc = NewService(pool, f.txs, f.pending, notifications.NewService(notifications.NewStore(pool)))
	f.txs.WithRecurringLinks(f.svc)
	f.pending.WithSubmitHook(f.svc.OnDraftSubmitted)
	f.savings = saving_goals.NewService(saving_goals.NewStore(pool))
	f.pending.WithSavings(f.savings)
	f.svc.WithSavings(f.savings)

	h := NewHandler(f.svc)
	th := transactions.NewHandler(f.txs)
	f.r = apitest.CountedEngine(f.me, func(g *gin.RouterGroup) {
		g.GET("/recurring", h.List)
		g.POST("/recurring", h.Create)
		g.POST("/recurring/occurrences/bulk", h.SettleOccurrences)
		g.GET("/recurring/:id", h.Get)
		g.PUT("/recurring/:id", h.Update)
		g.DELETE("/recurring/:id", h.Delete)
		g.GET("/recurring/:id/items", h.RuleItems)
		g.POST("/recurring/:id/pause", h.Pause)
		g.POST("/recurring/:id/resume", h.Resume)
		g.POST("/recurring/:id/archive", h.Archive)
		g.POST("/recurring/:id/restore", h.Restore)
		g.POST("/recurring/:id/extend", h.Extend)
		g.POST("/recurring/:id/duplicate", h.Duplicate)
		g.GET("/recurring/:id/occurrences", h.Occurrences)
		g.POST("/recurring/:id/occurrences/confirm-ready", h.ConfirmReady)
		g.POST("/recurring/:id/occurrences/mark-paid-ready", h.MarkPaidReady)
		g.POST("/recurring/:id/occurrences/skip-ready", h.SkipReady)
		g.PUT("/recurring/:id/occurrences/:slot", h.EditOcc)
		g.POST("/recurring/:id/occurrences/:slot/confirm", h.ConfirmOcc)
		g.POST("/recurring/:id/occurrences/:slot/skip", h.SkipOcc)
		g.POST("/recurring/:id/occurrences/:slot/restore", h.RestoreOcc)
		g.POST("/recurring/:id/occurrences/:slot/mark-paid", h.MarkPaidOcc)
		g.POST("/recurring/:id/occurrences/:slot/unmark-paid", h.UnmarkPaidOcc)
		g.POST("/recurring/:id/occurrences/:slot/link", h.LinkOcc)
		g.POST("/recurring/:id/occurrences/:slot/unlink", h.UnlinkOcc)
		g.GET("/recurring-items", h.Items)
		g.POST("/recurring-items", h.CreateItem)
		g.GET("/recurring-items/:id", h.GetItem)
		g.PUT("/recurring-items/:id", h.UpdateItem)
		g.DELETE("/recurring-items/:id", h.DeleteItem)
		g.POST("/recurring-items/:id/link", h.Link)
		g.POST("/recurring-items/:id/archive", h.ArchiveItem)
		g.POST("/recurring-items/:id/restore", h.RestoreItem)
		g.POST("/transactions", th.Create)
		g.GET("/transactions/:id", th.Get)
		g.PUT("/transactions/:id", th.Update)
		g.DELETE("/transactions/:id", th.Delete)
	})
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM recurring_items WHERE user_id = $1`, f.me)
		_, _ = pool.Exec(ctx, `DELETE FROM recurring_rules WHERE user_id = $1`, f.me)
		_, _ = pool.Exec(ctx, `DELETE FROM pending_transactions WHERE user_id = $1`, f.me)
		_, _ = pool.Exec(ctx, `DELETE FROM notifications WHERE recipient_user_id = $1`, f.me)
		_, _ = pool.Exec(ctx, `DELETE FROM recurring_due_checks WHERE user_id = $1`, f.me)
	})
	return f
}

func (f *recFix) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, q, args...); err != nil {
		f.t.Fatalf("exec %q: %v", q, err)
	}
}

func (f *recFix) day(n int) string { return f.today.AddDate(0, 0, n).Format(dateLayout) }

func (f *recFix) call(method, path, op string, body any, want int, out any) []byte {
	f.t.Helper()
	status, raw := apitest.Do(f.t, f.r, method, "/api/v1"+path, body)
	if status != want {
		f.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, raw)
	}
	if op != "" {
		apitest.Conform(f.t, op, status, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			f.t.Fatal(err)
		}
	}
	return raw
}

func (f *recFix) balance() float64 {
	f.t.Helper()
	var b float64
	if err := f.pool.QueryRow(f.ctx, `SELECT balance FROM accounts WHERE id = $1`, f.wallet).Scan(&b); err != nil {
		f.t.Fatal(err)
	}
	return b
}

// shownBalance is the wallet's balance as the app shows it: the cached balance
// minus transactions dated after today.
func (f *recFix) shownBalance() float64 {
	f.t.Helper()
	bs, err := shared.LoadWalletBalances(f.ctx, f.pool, []uuid.UUID{f.wallet})
	if err != nil || len(bs) != 1 {
		f.t.Fatalf("wallet balance: %v %+v", err, bs)
	}
	return bs[0].Balance
}

func (f *recFix) items(ruleID uuid.UUID, tab string) []Item {
	f.t.Helper()
	var out ItemList
	f.call("GET", "/recurring/"+ruleID.String()+"/items?per_page=100&tab="+tab, "listRecurringRuleItems", nil, 200, &out)
	return out.Data
}

func (f *recFix) createWeekly(count int) Detail {
	f.t.Helper()
	body := map[string]any{
		"name": "Netflix", "type": "expense", "amount": 100, "account_id": f.wallet, "category_id": f.food,
		"rule": map[string]any{"freq": "daily", "interval": 7}, "start_date": f.day(-14),
		"end": map[string]any{"kind": "count", "count": count},
	}
	apitest.ConformRequest(f.t, "createRecurring", body)
	var d Detail
	f.call("POST", "/recurring", "createRecurring", body, 201, &d)
	return d
}


// track makes a rule count its past: occurrences before it was created are
// history (paid implicitly), so a test that wants overdue ones moves
// tracked_from back to the start.
func (f *recFix) track(id uuid.UUID) {
	f.t.Helper()
	f.exec(`UPDATE recurring_rules SET tracked_from = start_date WHERE id = $1`, id)
}

func (f *recFix) occ(id uuid.UUID, slot string) string {
	return "/recurring/" + id.String() + "/occurrences/" + slot
}

func (f *recFix) newTx(amount float64, day int) uuid.UUID {
	f.t.Helper()
	res, err := f.txs.Create(f.ctx, f.me, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &f.wallet, Amount: amount, Date: f.day(day)})
	if err != nil {
		f.t.Fatal(err)
	}
	return res.(*transactions.TransactionDetail).ID
}

func (f *recFix) storedRows(id uuid.UUID) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM recurring_items WHERE rule_id = $1`, id).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *recFix) notices(typ string) (rows, count int) {
	f.t.Helper()
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*), COALESCE(MAX((payload->>'count')::int), 0) FROM notifications
		WHERE recipient_user_id = $1 AND type = $2`, f.me, typ).Scan(&rows, &count); err != nil {
		f.t.Fatal(err)
	}
	return
}

// Upcoming occurrences are computed, not stored; a due one is a state.
func TestComputedOccurrences(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6) // daily / 7 from 14 days ago: -14 -7 0 +7 +14 +21
	id := d.Recurring.ID
	if f.storedRows(id) != 0 {
		t.Fatalf("a new rule stored %d rows", f.storedRows(id))
	}
	// Created today: the two earlier ones are history (paid implicitly), today is due.
	if c := d.Recurring.Counts; c.Due != 1 || c.Upcoming != 3 || d.Recurring.Progress.Done != 2 ||
		d.Recurring.Progress.Total == nil || *d.Recurring.Progress.Total != 6 {
		t.Fatalf("counts %+v progress %+v", d.Recurring.Counts, d.Recurring.Progress)
	}
	f.track(id)
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, &d)
	if c := d.Recurring.Counts; c.Due != 3 || c.Upcoming != 3 || d.Recurring.Progress.Done != 0 || d.Recurring.DueCount != 3 {
		t.Fatalf("after track: counts %+v progress %+v", c, d.Recurring.Progress)
	}
	if d.Recurring.NextDate == nil || *d.Recurring.NextDate != f.day(7) {
		t.Errorf("next_date = %v, want %s", d.Recurring.NextDate, f.day(7))
	}
	if len(d.Recurring.Chain) != 6 || d.Recurring.Chain[0].Status != StatusDue || d.Recurring.Chain[5].Status != StatusUpcoming {
		t.Errorf("chain = %+v", d.Recurring.Chain)
	}
	if d.Recurring.CurrentOccurrence == nil || d.Recurring.CurrentOccurrence.Slot != f.day(-14) ||
		d.Recurring.CurrentOccurrence.Status != StatusDue || d.Recurring.CurrentOccurrence.ItemID != nil {
		t.Errorf("current occurrence = %+v", d.Recurring.CurrentOccurrence)
	}
	if d.Stats.RemainingTotal == nil || *d.Stats.RemainingTotal != 600 {
		t.Errorf("remaining_total = %v, want 600", d.Stats.RemainingTotal)
	}
	if f.storedRows(id) != 0 {
		t.Errorf("reading stored %d rows", f.storedRows(id))
	}

	var due ItemList
	f.call("GET", "/recurring-items?status=due", "listRecurringItems", nil, 200, &due)
	if len(due.Data) != 3 {
		t.Fatalf("due items = %d, want 3", len(due.Data))
	}
	for _, it := range due.Data {
		if it.ID != nil || it.Stored || it.RuleID != id || it.Status != StatusDue || it.Description == nil ||
			!strings.Contains(*it.Description, "Netflix") {
			t.Errorf("due item = %+v", it)
		}
	}
	up := f.items(id, "upcoming")
	if len(up) != 3 || up[0].Slot != f.day(7) || up[0].Seq == nil || *up[0].Seq != 4 {
		t.Fatalf("upcoming = %+v", up)
	}
	hist := f.items(id, "produced")
	if len(hist) != 3 { // the due ones show in the history tab
		t.Errorf("history = %d", len(hist))
	}

	// A rule without an end: a window of months from `from`, and next_from to go on.
	var m Detail
	f.call("POST", "/recurring", "createRecurring", map[string]any{"name": "Rent", "type": "expense", "amount": 5000, "account_id": f.wallet,
		"rule": map[string]any{"freq": "monthly", "interval": 1}, "start_date": f.day(0)}, 201, &m)
	if m.Recurring.Counts.Upcoming != 0 || m.Recurring.Progress.Total != nil || m.Recurring.EndingSoon || len(m.Recurring.Chain) != 0 {
		t.Errorf("no-end counts %+v progress %+v", m.Recurring.Counts, m.Recurring.Progress)
	}
	var w ItemList
	f.call("GET", "/recurring/"+m.Recurring.ID.String()+"/items?tab=upcoming&months=12", "listRecurringRuleItems", nil, 200, &w)
	if len(w.Data) < 11 || w.Window == nil || w.Window.NextFrom == nil {
		t.Fatalf("12-month window: %d items, window %+v", len(w.Data), w.Window)
	}
	var w2 ItemList
	f.call("GET", "/recurring/"+m.Recurring.ID.String()+"/items?tab=upcoming&months=12&from="+*w.Window.NextFrom, "listRecurringRuleItems", nil, 200, &w2)
	if len(w2.Data) < 11 || w2.Data[0].Date <= w.Data[len(w.Data)-1].Date {
		t.Errorf("next window starts %v after %v", w2.Data[0].Date, w.Data[len(w.Data)-1].Date)
	}
	f.call("GET", "/recurring/"+m.Recurring.ID.String()+"/items?tab=upcoming&from=nope", "listRecurringRuleItems", nil, 400, nil)
}

// Confirm, skip, paid, link, unlink and the tx delete options.
func TestOccurrenceActions(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6)
	id := d.Recurring.ID
	f.track(id)
	s1, s2, s3 := f.day(-14), f.day(-7), f.day(0)

	var w ItemWriteResult
	f.call("POST", f.occ(id, s1)+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 200, &w)
	if w.Item.Status != StatusDone || !w.Item.Stored || w.Item.ID == nil || w.Transaction == nil || f.balance() != 900 {
		t.Fatalf("confirm = %+v balance %v", w.Item, f.balance())
	}
	var tx struct {
		Date        string  `json:"date"`
		Description *string `json:"description"`
	}
	f.call("GET", "/transactions/"+w.Item.Transaction.ID.String(), "", nil, 200, &tx)
	if tx.Date != s1 || tx.Description == nil || !strings.Contains(*tx.Description, "Netflix") {
		t.Errorf("confirmed tx = %+v", tx)
	}
	f.call("POST", f.occ(id, s1)+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 409, nil)
	f.call("POST", f.occ(id, f.day(-3))+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 404, nil) // not an occurrence

	// skip / restore: the row goes again when nothing is left on it
	f.call("POST", f.occ(id, s2)+"/skip", "skipRecurringOccurrence", map[string]any{}, 200, &w)
	if w.Item.Status != StatusSkipped || !w.Item.Stored {
		t.Fatalf("skip = %+v", w.Item)
	}
	f.call("POST", f.occ(id, s2)+"/restore", "restoreRecurringOccurrence", nil, 200, &w)
	if w.Item.Status != StatusDue || w.Item.Stored || w.Item.ID != nil || f.storedRows(id) != 1 {
		t.Fatalf("restore = %+v rows %d", w.Item, f.storedRows(id))
	}
	// paid without a transaction
	f.call("POST", f.occ(id, s2)+"/mark-paid", "markRecurringOccurrencePaid", nil, 200, &w)
	if w.Item.Status != StatusPaid || w.Item.Transaction != nil {
		t.Fatalf("mark-paid = %+v", w.Item)
	}
	f.call("POST", f.occ(id, s2)+"/unmark-paid", "unmarkRecurringOccurrencePaid", nil, 200, &w)
	if w.Item.Status != StatusDue {
		t.Fatalf("unmark-paid = %+v", w.Item)
	}

	// link: the tx keeps its date and amount; the occurrence shows the tx amount
	f.call("POST", f.occ(id, s2)+"/mark-paid", "markRecurringOccurrencePaid", nil, 200, nil)
	tx1 := f.newTx(120, -2)
	var link LinkResult
	f.call("POST", f.occ(id, s2)+"/link", "linkRecurringOccurrence", map[string]any{"transaction_id": tx1}, 200, &link)
	if link.Item.Status != StatusDone || av(link.Item.Amount) != 120 || link.Item.Transaction == nil ||
		link.Item.Transaction.Date == nil || *link.Item.Transaction.Date != f.day(-2) || !link.AmountDiffers || av(link.RuleAmount) != 100 {
		t.Fatalf("link = %+v", link)
	}
	// unlink: cleared first → back to paid
	var ul UnlinkResult
	f.call("POST", f.occ(id, s2)+"/unlink", "unlinkRecurringOccurrence", nil, 200, &ul)
	if ul.Item.Status != StatusPaid || ul.Item.Transaction != nil {
		t.Fatalf("unlink of a cleared one = %+v", ul.Item)
	}
	f.call("POST", f.occ(id, s2)+"/unmark-paid", "unmarkRecurringOccurrencePaid", nil, 200, nil)
	// a swap needs replace
	f.call("POST", f.occ(id, s3)+"/link", "linkRecurringOccurrence", map[string]any{"transaction_id": tx1}, 200, nil)
	raw := f.call("POST", f.occ(id, s2)+"/link", "linkRecurringOccurrence", map[string]any{"transaction_id": tx1}, 409, nil)
	if !contains(raw, s3) {
		t.Errorf("409 should name the other occurrence %s: %s", s3, raw)
	}
	f.call("POST", f.occ(id, s2)+"/link", "linkRecurringOccurrence", map[string]any{"transaction_id": tx1, "replace": true}, 200, &link)
	var s3item ItemList
	f.call("GET", "/recurring-items?month="+f.today.Format("2006-01"), "listRecurringItems", nil, 200, &s3item)
	for _, it := range s3item.Data {
		if it.Slot == s3 && it.Status != StatusDue {
			t.Errorf("after the swap %s is %s, want due", s3, it.Status)
		}
	}
	// unlink of a never-cleared one → computed again (no row left)
	f.call("POST", f.occ(id, s2)+"/unlink", "unlinkRecurringOccurrence", nil, 200, &ul)
	if ul.Item.Status != StatusDue || ul.Item.Stored {
		t.Fatalf("unlink = %+v", ul.Item)
	}
	f.call("POST", f.occ(id, s2)+"/unlink", "unlinkRecurringOccurrence", nil, 400, nil)

	// deleting the tx: plain → the occurrence is due again; skip_occurrence → skipped
	confirmed := w.Transaction
	_ = confirmed
	var first ItemWriteResult
	f.call("POST", f.occ(id, s2)+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 200, &first)
	f.call("DELETE", "/transactions/"+first.Item.Transaction.ID.String(), "deleteTransaction", nil, 200, nil)
	var again Item
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM recurring_items WHERE rule_id = $1 AND slot = $2`, id, s2).Scan(new(int)); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/recurring-items?month="+f.today.Format("2006-01")+"&status=due", "listRecurringItems", nil, 200, nil)
	_ = again
	second := f.newTx(100, -1)
	f.call("POST", f.occ(id, s3)+"/link", "linkRecurringOccurrence", map[string]any{"transaction_id": second}, 200, nil)
	f.call("DELETE", "/transactions/"+second.String()+"?skip_occurrence=true", "deleteTransaction", nil, 200, nil)
	var skipped ItemList
	f.call("GET", "/recurring-items?status=skipped", "listRecurringItems", nil, 200, &skipped)
	if len(skipped.Data) != 1 || skipped.Data[0].Slot != s3 {
		t.Errorf("skipped after delete + skip_occurrence = %+v", skipped.Data)
	}
	// skip an occurrence that has a tx, deleting the tx too
	var txCount int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND amount = 100`, f.me).Scan(&txCount)
	f.call("POST", f.occ(id, s1)+"/skip", "skipRecurringOccurrence", map[string]any{"delete_tx": true}, 200, &w)
	var after int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND amount = 100`, f.me).Scan(&after)
	if w.Item.Status != StatusSkipped || w.Item.Transaction != nil || after != txCount-1 {
		t.Errorf("skip + delete_tx = %v tx %v, transactions %d → %d", w.Item.Status, w.Item.Transaction, txCount, after)
	}
}

func TestBulkOccurrences(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6)
	id := d.Recurring.ID
	f.track(id)
	var b BulkResult
	f.call("POST", "/recurring/"+id.String()+"/occurrences/confirm-ready", "confirmRecurringOccurrencesReady", nil, 200, &b)
	if len(b.Items) != 3 || len(b.LeftOut) != 0 || f.balance() != 700 {
		t.Fatalf("confirm-ready = %d items, left out %+v, balance %v", len(b.Items), b.LeftOut, f.balance())
	}
	for _, it := range b.Items {
		if it.Status != StatusDone || it.Transaction == nil {
			t.Errorf("item = %+v", it)
		}
	}
	// a reminder has no amount: confirm-ready leaves it out; mark-paid-ready and skip-ready take it
	var rem Detail
	f.call("POST", "/recurring", "createRecurring", map[string]any{"name": "Water", "kind": "reminder", "account_id": f.wallet,
		"rule": map[string]any{"freq": "monthly", "interval": 1}, "start_date": f.day(-40)}, 201, &rem)
	f.track(rem.Recurring.ID)
	f.call("POST", "/recurring/"+rem.Recurring.ID.String()+"/occurrences/confirm-ready", "confirmRecurringOccurrencesReady", nil, 200, &b)
	if len(b.Items) != 0 || len(b.LeftOut) == 0 || b.LeftOut[0].Reason != "needs_amount" {
		t.Fatalf("reminder confirm-ready = %+v", b)
	}
	f.call("POST", "/recurring/"+rem.Recurring.ID.String()+"/occurrences/mark-paid-ready", "markRecurringOccurrencesPaidReady", nil, 200, &b)
	if len(b.Items) == 0 || b.Items[0].Status != StatusPaid {
		t.Fatalf("mark-paid-ready = %+v", b.Items)
	}
	var due ItemList
	f.call("GET", "/recurring-items?status=due&rule_id="+rem.Recurring.ID.String(), "listRecurringItems", nil, 200, &due)
	// (the list also carries this month's upcoming ones without an amount: only the due ones count here)
	if n := len(onlyDue(due.Data)); n != 0 {
		t.Errorf("still due after mark-paid-ready: %d", n)
	}
	d2 := f.createWeekly(6)
	f.track(d2.Recurring.ID)
	f.call("POST", "/recurring/"+d2.Recurring.ID.String()+"/occurrences/skip-ready", "skipRecurringOccurrencesReady", nil, 200, &b)
	if len(b.Items) != 3 || b.Items[0].Status != StatusSkipped {
		t.Fatalf("skip-ready = %+v", b.Items)
	}
}

func TestSettleOccurrences(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6)
	a := d.Recurring.ID
	f.track(a)
	var rem Detail
	f.call("POST", "/recurring", "createRecurring", map[string]any{"name": "Water", "kind": "reminder", "account_id": f.wallet,
		"rule": map[string]any{"freq": "monthly", "interval": 1}, "start_date": f.day(-40)}, 201, &rem)
	b := rem.Recurring.ID
	f.track(b)
	var due ItemList
	f.call("GET", "/recurring-items?status=due&rule_id="+b.String(), "listRecurringItems", nil, 200, &due)
	remDue := onlyDue(due.Data)
	if len(remDue) < 2 {
		t.Fatalf("reminder due = %d, want 2+", len(remDue))
	}
	bulk := func(body map[string]any, want int) OccBulkResult {
		t.Helper()
		var out OccBulkResult
		if want == 200 {
			apitest.ConformRequest(t, "settleRecurringOccurrences", body)
			f.call("POST", "/recurring/occurrences/bulk", "settleRecurringOccurrences", body, 200, &out)
		} else {
			f.call("POST", "/recurring/occurrences/bulk", "", body, want, nil)
		}
		return out
	}
	item := func(rule uuid.UUID, slot string, amount ...float64) map[string]any {
		m := map[string]any{"rule_id": rule, "slot": slot}
		if len(amount) > 0 {
			m["amount"] = amount[0]
		}
		return m
	}

	// confirm across two settings: the ones with an amount go, the rest are left out with the reason
	out := bulk(map[string]any{"action": "confirm", "items": []any{
		item(a, f.day(-14)), item(a, f.day(-7)), item(b, remDue[0].Slot), item(uuid.New(), f.day(-7)), item(a, f.day(-14)),
	}}, 200)
	reasons := map[string]int{}
	for _, lo := range out.LeftOut {
		reasons[lo.Reason]++
	}
	if len(out.Items) != 2 || reasons["needs_amount"] != 1 || reasons["not_found"] != 1 || reasons["already_recorded"] != 1 || f.balance() != 800 {
		t.Fatalf("confirm = %d items, left out %+v, balance %v", len(out.Items), out.LeftOut, f.balance())
	}
	if out.Items[0].Status != StatusDone || out.Items[0].Transaction == nil || len(out.Settings) != 1 || out.Settings[0].ID != a || len(out.AccountBalances) != 1 {
		t.Fatalf("confirm result = %+v / settings %d / balances %+v", out.Items[0], len(out.Settings), out.AccountBalances)
	}
	// an amount typed for one that has none records it
	out = bulk(map[string]any{"action": "confirm", "items": []any{item(b, remDue[0].Slot, 50)}}, 200)
	if len(out.Items) != 1 || out.Items[0].Status != StatusDone || f.balance() != 750 {
		t.Fatalf("confirm with amount = %+v, balance %v", out, f.balance())
	}
	// mark_paid and skip take a row with no amount; a recorded one is left out
	out = bulk(map[string]any{"action": "mark_paid", "items": []any{item(b, remDue[1].Slot), item(a, f.day(-7))}}, 200)
	if len(out.Items) != 1 || out.Items[0].Status != StatusPaid || len(out.LeftOut) != 1 || out.LeftOut[0].Reason != "already_recorded" {
		t.Fatalf("mark_paid = %+v", out)
	}
	out = bulk(map[string]any{"action": "skip", "items": []any{item(a, f.day(0))}}, 200)
	if len(out.Items) != 1 || out.Items[0].Status != StatusSkipped || f.balance() != 750 {
		t.Fatalf("skip = %+v", out)
	}
	// a skipped one can't be confirmed
	out = bulk(map[string]any{"action": "confirm", "items": []any{item(a, f.day(0))}}, 200)
	if len(out.Items) != 0 || len(out.LeftOut) != 1 || out.LeftOut[0].Reason != "refused" {
		t.Fatalf("confirm skipped = %+v", out)
	}

	bulk(map[string]any{"action": "delete", "items": []any{item(a, f.day(0))}}, 400)
	bulk(map[string]any{"action": "skip", "items": []any{item(a, f.day(0), 10)}}, 400)
	bulk(map[string]any{"action": "confirm", "items": []any{item(a, f.day(0), 0)}}, 400)
	bulk(map[string]any{"action": "confirm", "items": []any{}}, 400)
}

func TestAutoCreate(t *testing.T) {
	f := newRecFix(t)
	body := map[string]any{"name": "Gym", "type": "expense", "amount": 50, "account_id": f.wallet,
		"rule": map[string]any{"freq": "daily", "interval": 1}, "start_date": f.day(-3), "auto_create": true}
	var d Detail
	f.call("POST", "/recurring", "createRecurring", body, 201, &d)
	id := d.Recurring.ID
	if !d.Recurring.AutoCreate {
		t.Fatalf("auto_create = %v", d.Recurring.AutoCreate)
	}
	// Today's occurrence came due after it was switched on: recorded; earlier ones are history.
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, &d)
	if f.balance() != 950 {
		t.Fatalf("balance = %v, want 950 (today's occurrence)", f.balance())
	}
	var items ItemList
	f.call("GET", "/recurring-items?status=done", "listRecurringItems", nil, 200, &items)
	if len(items.Data) != 1 || !items.Data[0].AutoCreated || items.Data[0].Slot != f.day(0) || items.Data[0].Transaction == nil {
		t.Fatalf("auto-created = %+v", items.Data)
	}
	if rows, count := f.notices("recurring_auto_created"); rows != 1 || count != 1 {
		t.Errorf("auto notice rows %d count %d", rows, count)
	}
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, nil)
	if f.balance() != 950 {
		t.Errorf("a second check made more: %v", f.balance())
	}
	// Deleting the tx: due again, never re-created.
	f.call("DELETE", "/transactions/"+items.Data[0].Transaction.ID.String(), "deleteTransaction", nil, 200, nil)
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, &d)
	if f.balance() != 1000 || d.Recurring.Counts.Due != 1 {
		t.Errorf("after delete: balance %v due %d", f.balance(), d.Recurring.Counts.Due)
	}

	// validation
	noAmount := map[string]any{"name": "W", "kind": "reminder", "account_id": f.wallet, "auto_create": true,
		"rule": map[string]any{"freq": "monthly", "interval": 1}, "start_date": f.day(0)}
	if raw := f.call("POST", "/recurring", "createRecurring", noAmount, 400, nil); !contains(raw, "AUTO_CREATE_NEEDS_AMOUNT") {
		t.Errorf("no amount: %s", raw)
	}
	noWallet := map[string]any{"name": "W", "type": "expense", "amount": 5, "auto_create": true,
		"rule": map[string]any{"freq": "monthly", "interval": 1}, "start_date": f.day(0)}
	if raw := f.call("POST", "/recurring", "createRecurring", noWallet, 400, nil); !contains(raw, "AUTO_CREATE_NEEDS_WALLET") {
		t.Errorf("no wallet: %s", raw)
	}

	// Switching it on later never back-creates: the old due ones stay due.
	var old Detail
	f.call("POST", "/recurring", "createRecurring", map[string]any{"name": "Old", "type": "expense", "amount": 10, "account_id": f.wallet,
		"rule": map[string]any{"freq": "daily", "interval": 1}, "start_date": f.day(-5)}, 201, &old)
	f.track(old.Recurring.ID)
	f.call("PUT", "/recurring/"+old.Recurring.ID.String(), "updateRecurring", map[string]any{"auto_create": true}, 200, &old)
	f.call("GET", "/recurring/"+old.Recurring.ID.String(), "getRecurring", nil, 200, &old)
	if old.Recurring.Counts.Due != 5 || old.Recurring.Counts.Done != 1 { // the 5 past ones due, today recorded
		t.Errorf("late switch: counts %+v", old.Recurring.Counts)
	}
}

func TestDueNoticeResumeAndEdit(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6)
	id := d.Recurring.ID
	f.track(id)
	f.exec(`UPDATE recurring_rules SET due_notified_until = $2 WHERE id = $1`, id, f.today.AddDate(0, 0, -30))
	f.exec(`DELETE FROM notifications WHERE recipient_user_id = $1`, f.me)
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, nil)
	if rows, _ := f.notices("recurring_due"); rows != 0 {
		t.Errorf("a GET made a recurring_due notice (rows %d)", rows)
	}
	if err := f.svc.DueCheckDaily(f.ctx, f.me, ""); err != nil {
		t.Fatal(err)
	}
	if rows, _ := f.notices("recurring_due"); rows != 0 {
		t.Errorf("the day was already checked, yet a notice was made (rows %d)", rows)
	}
	f.exec(`DELETE FROM recurring_due_checks WHERE user_id = $1`, f.me)
	if err := f.svc.DueCheckDaily(f.ctx, f.me, ""); err != nil {
		t.Fatal(err)
	}
	if rows, count := f.notices("recurring_due"); rows != 1 || count != 3 {
		t.Errorf("recurring_due rows %d count %d, want 1 / 3", rows, count)
	}
	var dl string
	_ = f.pool.QueryRow(f.ctx, `SELECT COALESCE(deep_link, '') FROM notifications WHERE recipient_user_id = $1 AND type = 'recurring_due'`, f.me).Scan(&dl)
	if dl != "/recurring?focus=due" {
		t.Errorf("deep link = %q", dl)
	}
	if err := f.svc.DueCheck(f.ctx, f.me, ""); err != nil {
		t.Fatal(err)
	}
	if rows, count := f.notices("recurring_due"); rows != 1 || count != 3 {
		t.Errorf("a second check made another notice: rows %d count %d", rows, count)
	}

	// Pause + resume: what was due before the pause stays due; the occurrences of the
	// paused gap are skipped. (The rule is daily / 7: -14, -7, 0, +7 …; paused "since" 8 days ago.)
	f.call("POST", "/recurring/"+id.String()+"/pause", "pauseRecurring", nil, 200, nil)
	f.exec(`UPDATE recurring_rules SET paused_from = $2 WHERE id = $1`, id, f.today.AddDate(0, 0, -8))
	f.call("POST", "/recurring/"+id.String()+"/resume", "resumeRecurring", nil, 200, &d)
	var tf string
	_ = f.pool.QueryRow(f.ctx, `SELECT to_char(tracked_from, 'YYYY-MM-DD') FROM recurring_rules WHERE id = $1`, id).Scan(&tf)
	if tf != f.day(-14) {
		t.Errorf("resume moved tracked_from to %s", tf)
	}
	if c := d.Recurring.Counts; c.Due != 2 || c.Skipped != 1 || c.Paid != 0 { // -14 and today stay due, -7 is in the gap
		t.Errorf("after resume: counts %+v", c)
	}
	var skippedSlot string
	_ = f.pool.QueryRow(f.ctx, `SELECT to_char(slot, 'YYYY-MM-DD') FROM recurring_items WHERE rule_id = $1 AND archived`, id).Scan(&skippedSlot)
	if skippedSlot != f.day(-7) {
		t.Errorf("skipped slot = %s, want %s", skippedSlot, f.day(-7))
	}
}

func onlyDue(items []Item) []Item {
	var out []Item
	for _, it := range items {
		if it.Status == StatusDue {
			out = append(out, it)
		}
	}
	return out
}

func TestRuleEditPushesToRecordedTx(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6)
	id := d.Recurring.ID
	f.track(id)
	var a, b ItemWriteResult
	f.call("POST", f.occ(id, f.day(-14))+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 200, &a)
	f.call("POST", f.occ(id, f.day(-7))+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 200, &b)
	// the second one's description was changed by the user: the rename leaves it alone
	f.call("PUT", "/transactions/"+b.Item.Transaction.ID.String(), "", map[string]any{"description": "mine"}, 200, nil)

	var upd Detail
	f.call("PUT", "/recurring/"+id.String(), "updateRecurring", map[string]any{"amount": 999}, 200, &upd)
	if upd.TxUpdate != nil {
		t.Errorf("tx_update without also_update_tx: %+v", upd.TxUpdate)
	}
	f.call("PUT", "/recurring/"+id.String(), "updateRecurring", map[string]any{"name": "Netflix 4K", "also_update_tx": true}, 200, &upd)
	if upd.TxUpdate == nil || upd.TxUpdate.UpdatedCount != 1 || len(upd.TxUpdate.Failed) != 0 {
		t.Fatalf("tx_update = %+v", upd.TxUpdate)
	}
	read := func(txID uuid.UUID) (desc *string, amount float64, date string) {
		var row struct {
			Description *string `json:"description"`
			Amount      float64 `json:"amount"`
			Date        string  `json:"date"`
		}
		f.call("GET", "/transactions/"+txID.String(), "", nil, 200, &row)
		return row.Description, row.Amount, row.Date
	}
	da, aa, dd := read(a.Item.Transaction.ID)
	if da == nil || !strings.Contains(*da, "Netflix 4K") || aa != 100 || dd != f.day(-14) {
		t.Errorf("tx a = %v %v %v (name follows, amount / date never)", da, aa, dd)
	}
	if db, _, _ := read(b.Item.Transaction.ID); db == nil || *db != "mine" {
		t.Errorf("tx b description = %v, want mine", db)
	}
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, &upd)
	if upd.Recurring.RecordedTxCount != 2 {
		t.Errorf("recorded_tx_count = %d", upd.Recurring.RecordedTxCount)
	}
}

func TestOccurrencesPicker(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6)
	id := d.Recurring.ID
	f.track(id)
	var res OccurrencesResult
	f.call("GET", "/recurring/"+id.String()+"/occurrences?tx_date="+f.day(8)+"&past=1&upcoming=2", "listRecurringOccurrences", nil, 200, &res)
	if len(res.Data) != 5 || res.SuggestedSlot == nil || *res.SuggestedSlot != f.day(-14) {
		t.Fatalf("picker = %d items, suggested %v", len(res.Data), res.SuggestedSlot)
	}
	var cp Detail
	f.call("POST", "/recurring/"+id.String()+"/occurrences/confirm-ready", "confirmRecurringOccurrencesReady", nil, 200, nil)
	f.call("GET", "/recurring/"+id.String()+"/occurrences?tx_date="+f.day(8), "listRecurringOccurrences", nil, 200, &res)
	if res.SuggestedSlot == nil || *res.SuggestedSlot != f.day(7) {
		t.Errorf("nothing overdue: suggested %v, want the one nearest the tx date %s", res.SuggestedSlot, f.day(7))
	}
	_ = cp
	// the rule list: overdue first
	var list RecurringList
	f.call("GET", "/recurring?for_type=expense", "listRecurring", nil, 200, &list)
	if len(list.Data) != 1 || list.Data[0].DueCount != 0 {
		t.Errorf("list = %+v", list.Data)
	}
}

func TestRecurringValidation(t *testing.T) {
	f := newRecFix(t)
	bad := map[string]any{"name": "X", "type": "expense", "amount": 10, "account_id": f.wallet,
		"rule": map[string]any{"freq": "monthly"}, "start_date": f.day(0),
		"end": map[string]any{"kind": "date", "date": f.today.AddDate(3, 0, 0).Format(dateLayout)}}
	raw := f.call("POST", "/recurring", "createRecurring", bad, 400, nil)
	if !contains(raw, "END_TOO_FAR") {
		t.Errorf("end too far: %s", raw)
	}
	bad["end"] = nil
	bad["rule"] = map[string]any{"freq": "hourly"}
	if raw := f.call("POST", "/recurring", "createRecurring", bad, 400, nil); !contains(raw, "INVALID_RULE") {
		t.Errorf("invalid rule: %s", raw)
	}
	bad["rule"] = map[string]any{"freq": "monthly"}
	bad["category_id"] = uuid.New()
	if raw := f.call("POST", "/recurring", "createRecurring", bad, 400, nil); !contains(raw, "INVALID_CATEGORY") {
		t.Errorf("invalid category: %s", raw)
	}
	delete(bad, "category_id")
	var d Detail
	f.call("POST", "/recurring", "createRecurring", bad, 201, &d)
	if d.Recurring.Counts.Upcoming != 0 || d.Recurring.Progress.Total != nil {
		t.Errorf("no end monthly: %+v", d.Recurring)
	}
	// nothing to extend without an end
	f.call("POST", "/recurring/"+d.Recurring.ID.String()+"/extend", "extendRecurring", map[string]any{}, 409, nil)
	bad["rule"] = map[string]any{"freq": "dates", "dates": []string{f.day(5), f.day(9), f.day(5)}}
	f.call("POST", "/recurring", "createRecurring", bad, 201, &d)
	if d.Recurring.Counts.Upcoming != 2 || d.Recurring.Progress.Total == nil || *d.Recurring.Progress.Total != 2 {
		t.Errorf("dates: %+v", d.Recurring)
	}
	// an end can be pushed out
	var e Detail
	f.call("POST", "/recurring", "createRecurring", map[string]any{"name": "E", "type": "expense", "amount": 10, "account_id": f.wallet,
		"rule": map[string]any{"freq": "daily", "interval": 7}, "start_date": f.day(0), "end": map[string]any{"kind": "count", "count": 3}}, 201, &e)
	f.call("POST", "/recurring/"+e.Recurring.ID.String()+"/extend", "extendRecurring", map[string]any{"end": map[string]any{"kind": "count", "count": 5}}, 200, &e)
	if e.Recurring.Progress.Total == nil || *e.Recurring.Progress.Total != 5 {
		t.Errorf("extended end: %+v", e.Recurring.Progress)
	}
}

func TestRecurringKinds(t *testing.T) {
	f := newRecFix(t)
	monthly := map[string]any{"freq": "monthly", "interval": 1}
	mk := func(body map[string]any, want int) Detail {
		t.Helper()
		var d Detail
		body["rule"], body["start_date"] = monthly, f.day(-1)
		if _, ok := body["name"]; !ok {
			body["name"] = "X"
		}
		f.call("POST", "/recurring", "createRecurring", body, want, &d)
		return d
	}
	sub := mk(map[string]any{"kind": "subscription", "amount": 100, "account_id": f.wallet, "trial_until": f.day(14)}, 201)
	if sub.Recurring.Kind != "subscription" || sub.Recurring.Type != "expense" || sub.Recurring.YearlyCost != 1200 ||
		sub.Recurring.TrialUntil == nil || *sub.Recurring.TrialUntil != f.day(14) {
		t.Errorf("subscription = %+v", sub.Recurring)
	}
	mk(map[string]any{"kind": "installment", "amount": 500}, 400)
	inst := mk(map[string]any{"name": "Phone", "kind": "installment", "amount": 500, "account_id": f.wallet,
		"end": map[string]any{"kind": "count", "count": 6}}, 201)
	if inst.Recurring.Kind != "installment" || inst.Recurring.YearlyCost != 3000 || inst.Recurring.Progress.Total == nil ||
		*inst.Recurring.Progress.Total != 6 {
		t.Errorf("installment = %+v yearly %v", inst.Recurring.Progress, inst.Recurring.YearlyCost)
	}
	inc := mk(map[string]any{"name": "Salary", "kind": "income", "amount": 30000, "account_id": f.wallet}, 201)
	if inc.Recurring.Type != "income" || inc.Recurring.YearlyCost != 360000 {
		t.Errorf("income = %+v", inc.Recurring)
	}
	mk(map[string]any{"kind": "income", "type": "expense", "amount": 5}, 400) // an income kind is an income
	rem := mk(map[string]any{"name": "Electricity", "kind": "reminder", "account_id": f.wallet}, 201)
	if rem.Recurring.Kind != "reminder" || rem.Recurring.Amount != nil || rem.Recurring.YearlyCost != 0 {
		t.Errorf("reminder = %+v", rem.Recurring)
	}
	// a reminder cannot be confirmed without an amount; with one it can
	var occs ItemList
	f.call("GET", "/recurring-items?status=due&rule_id="+rem.Recurring.ID.String(), "listRecurringItems", nil, 200, &occs)
	_ = occs
	f.track(rem.Recurring.ID)
	f.call("GET", "/recurring-items?status=due&rule_id="+rem.Recurring.ID.String(), "listRecurringItems", nil, 200, &occs)
	dueNow := onlyDue(occs.Data) // not this month's upcoming ones (they are in the list too)
	if len(dueNow) != 1 || !dueNow[0].NeedsAmount {
		t.Fatalf("reminder due = %+v", occs.Data)
	}
	slot := dueNow[0].Slot
	if raw := f.call("POST", f.occ(rem.Recurring.ID, slot)+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 400, nil); !contains(raw, "AMOUNT_REQUIRED") {
		t.Errorf("confirm without amount: %s", raw)
	}
	f.call("POST", f.occ(rem.Recurring.ID, slot)+"/confirm", "confirmRecurringOccurrence", map[string]any{"amount": 321}, 200, nil)
	other := testdb.Account(t, f.pool, f.me)
	mk(map[string]any{"name": "Move", "kind": "reminder", "type": "transfer", "account_id": f.wallet, "to_account_id": other}, 201)
	mk(map[string]any{"kind": "bogus", "amount": 5}, 400)

	var list RecurringList
	f.call("GET", "/recurring", "listRecurring", nil, 200, &list)
	k := list.Summary.ByKind
	if k.Subscription.Count != 1 || k.Subscription.Yearly != 1200 || k.Installment.Count != 1 || k.Income.Count != 1 ||
		k.Income.Monthly != 30000 || k.Reminder.Count != 2 {
		t.Errorf("by_kind = %+v", k)
	}
	if list.Summary.Due30dTotal <= 0 {
		t.Errorf("due_30d_total = %v", list.Summary.Due30dTotal)
	}
}

func TestRecurringCalendarMonth(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6) // every 7 days, started 14 days ago
	f.track(d.Recurring.ID)
	month := f.today.Format("2006-01")
	var l ItemList
	f.call("GET", "/recurring-items?month="+month, "listRecurringItems", nil, 200, &l)
	first, last := f.today.AddDate(0, 0, -f.today.Day()+1), f.today.AddDate(0, 1, -f.today.Day())
	for i, it := range l.Data {
		if it.Date < first.Format("2006-01-02") || it.Date > last.Format("2006-01-02") {
			t.Errorf("item %s outside %s", it.Date, month)
		}
		if i > 0 && l.Data[i-1].Date > it.Date {
			t.Errorf("not oldest first: %s before %s", l.Data[i-1].Date, it.Date)
		}
	}
	if l.Pagination.PerPage != 100 {
		t.Errorf("per_page = %d, want 100 for a month", l.Pagination.PerPage)
	}
	f.call("GET", "/recurring-items?month=2026-13", "listRecurringItems", nil, 400, nil)
}

// A stored (edited) occurrence replaces the computed one of the same period.
func TestEditedOccurrenceReplacesComputed(t *testing.T) {
	f := newRecFix(t)
	var m Detail
	f.call("POST", "/recurring", "createRecurring", map[string]any{"name": "Rent", "type": "expense", "amount": 5000, "account_id": f.wallet,
		"rule": map[string]any{"freq": "monthly", "interval": 1}, "start_date": f.day(0)}, 201, &m)
	id := m.Recurring.ID
	up := f.items(id, "upcoming")
	if len(up) < 2 {
		t.Fatalf("upcoming = %d", len(up))
	}
	slot := up[0].Slot
	var w ItemWriteResult
	f.call("PUT", f.occ(id, slot), "updateRecurringOccurrence", map[string]any{"amount": 4800, "date": up[0].Date}, 200, &w)
	if !w.Item.Stored || !w.Item.Edited || av(w.Item.Amount) != 4800 {
		t.Fatalf("edit = %+v", w.Item)
	}
	up = f.items(id, "upcoming")
	n := 0
	for _, it := range up {
		if it.Slot == slot {
			n++
			if av(it.Amount) != 4800 {
				t.Errorf("edited occurrence amount = %v", av(it.Amount))
			}
		}
	}
	if n != 1 {
		t.Errorf("the edited occurrence shows %d times", n)
	}
	// a rule edit changes the unstored ones only
	f.call("PUT", "/recurring/"+id.String(), "updateRecurring", map[string]any{"amount": 5100}, 200, nil)
	up = f.items(id, "upcoming")
	for _, it := range up {
		want := 5100.0
		if it.Slot == slot {
			want = 4800
		}
		if av(it.Amount) != want {
			t.Errorf("%s amount = %v, want %v", it.Slot, av(it.Amount), want)
		}
	}
}

// A saving setting records a saving entry (never a transaction), counts as neither
// expense nor income, follows its goal's life and is the goal's month plan.
func TestRecurringSaving(t *testing.T) {
	f := newRecFix(t)
	goal := uuid.New()
	f.exec(`INSERT INTO saving_goals (id, user_id, name, target_amount) VALUES ($1, $2, 'Japan', 80000)`, goal, f.me)
	monthly := map[string]any{"freq": "monthly", "interval": 1}
	body := func() map[string]any {
		return map[string]any{"name": "ออม Japan", "kind": "saving", "amount": 500, "account_id": f.wallet,
			"goal_id": goal, "rule": monthly, "start_date": f.day(-1)}
	}
	noWallet := body()
	delete(noWallet, "account_id")
	if raw := f.call("POST", "/recurring", "createRecurring", noWallet, 400, nil); !contains(raw, "SAVING_NEEDS_WALLET") {
		t.Errorf("no wallet: %s", raw)
	}
	untracked := testdb.Account(t, f.pool, f.me)
	f.exec(`UPDATE accounts SET track_balance = FALSE WHERE id = $1`, untracked)
	bad := body()
	bad["account_id"] = untracked
	if raw := f.call("POST", "/recurring", "createRecurring", bad, 400, nil); !contains(raw, "WALLET_NOT_TRACKED") {
		t.Errorf("untracked wallet: %s", raw)
	}

	apitest.ConformRequest(t, "createRecurring", body())
	var d Detail
	f.call("POST", "/recurring", "createRecurring", body(), 201, &d)
	r := d.Recurring
	if r.Type != "saving" || r.Kind != KindSaving || r.GoalID == nil || *r.GoalID != goal || r.Goal == nil ||
		r.Goal.Name != "Japan" || r.AutoCreate || r.Category != nil {
		t.Fatalf("saving setting = %+v", r)
	}
	id := r.ID
	f.track(id)
	var due ItemList
	f.call("GET", "/recurring-items?status=due&rule_id="+id.String(), "listRecurringItems", nil, 200, &due)
	dueNow := onlyDue(due.Data)
	if len(dueNow) != 1 || dueNow[0].Type != "saving" || dueNow[0].GoalID == nil || dueNow[0].Goal == nil {
		t.Fatalf("due = %+v", due.Data)
	}
	slot := dueNow[0].Slot

	// an existing transaction can't be its record
	f.call("POST", f.occ(id, slot)+"/link", "", map[string]any{"transaction_id": f.newTx(500, 0)}, 400, nil)
	before := f.balance()
	var w ItemWriteResult
	f.call("POST", f.occ(id, slot)+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 200, &w)
	if w.Item.Status != StatusDone || w.Item.SavingEntryID == nil || av(w.Item.Amount) != 500 || len(w.AccountBalances) != 1 {
		t.Fatalf("confirmed = %+v", w)
	}
	var (
		toGoal *uuid.UUID
		acc    *uuid.UUID
		amount float64
		source string
	)
	if err := f.pool.QueryRow(f.ctx, `SELECT to_goal_id, account_id, amount::float8, source FROM saving_entries WHERE id = $1`,
		*w.Item.SavingEntryID).Scan(&toGoal, &acc, &amount, &source); err != nil {
		t.Fatal(err)
	}
	if toGoal == nil || *toGoal != goal || acc == nil || *acc != f.wallet || amount != 500 || source != "recurring" {
		t.Errorf("entry = goal %v wallet %v amount %v source %s", toGoal, acc, amount, source)
	}
	if f.balance() != before {
		t.Errorf("a saving moved the wallet balance: %v → %v", before, f.balance())
	}
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, &d)
	if d.Stats.PaidCount != 1 || d.Stats.PaidTotal != 500 || d.Recurring.AvgAmount == nil || *d.Recurring.AvgAmount != 500 {
		t.Errorf("stats = %+v", d.Stats)
	}
	var list RecurringList
	f.call("GET", "/recurring", "listRecurring", nil, 200, &list)
	if list.Summary.MonthPay != 0 || list.Summary.MonthReceive != 0 || list.Summary.ByKind.Saving.Count != 1 ||
		list.Summary.ByKind.Subscription.Count != 0 {
		t.Errorf("summary = %+v", list.Summary)
	}
	exp, err := f.svc.ExpectedBetween(f.ctx, f.me, f.today, f.today.AddDate(0, 2, 0))
	if err != nil || len(exp) == 0 || exp[0].Type != "saving" {
		t.Errorf("expected = %+v (%v)", exp, err)
	}
	page, err := f.savings.Get(f.ctx, f.me, goal, 20, false)
	if err != nil || page.Goal.MonthPlan == nil || *page.Goal.MonthPlan != 500 {
		t.Errorf("goal month_plan = %v (%v)", page.Goal.MonthPlan, err)
	}

	// skipping with delete_tx deletes the saving entry
	f.call("POST", f.occ(id, slot)+"/skip", "skipRecurringOccurrence", map[string]any{"delete_tx": true}, 200, nil)
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM saving_entries WHERE user_id = $1`, f.me).Scan(&n)
	if n != 0 {
		t.Errorf("entries after skip = %d", n)
	}

	// the unallocated pot
	spare := body()
	spare["name"], spare["goal_id"] = "ออมเงิน", nil
	var sd Detail
	f.call("POST", "/recurring", "createRecurring", spare, 201, &sd)
	if sd.Recurring.GoalID != nil || sd.Recurring.Goal != nil || sd.Recurring.Kind != KindSaving {
		t.Fatalf("spare setting = %+v", sd.Recurring)
	}
	f.track(sd.Recurring.ID)
	f.call("POST", f.occ(sd.Recurring.ID, slot)+"/confirm", "confirmRecurringOccurrence", map[string]any{}, 200, &w)
	var bucket string
	_ = f.pool.QueryRow(f.ctx, `SELECT to_bucket FROM saving_entries WHERE id = $1`, *w.Item.SavingEntryID).Scan(&bucket)
	if bucket != "spare" {
		t.Errorf("spare saving went to %q", bucket)
	}

	// archiving the goal pauses its setting; deleting the goal deletes it
	if _, err := f.savings.Archive(f.ctx, f.me, goal); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = f.pool.QueryRow(f.ctx, `SELECT status FROM recurring_rules WHERE id = $1`, id).Scan(&status)
	if status != "paused" {
		t.Errorf("setting of an archived goal = %s", status)
	}
	if raw := f.call("POST", "/recurring/"+id.String()+"/resume", "", nil, 409, nil); !contains(raw, "GOAL_NOT_ACTIVE") {
		t.Errorf("resume on an archived goal: %s", raw)
	}
	if err := f.savings.Delete(f.ctx, f.me, goal); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/recurring/"+id.String(), "", nil, 404, nil)
}

func av(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func TestRecurringDuplicate(t *testing.T) {
	f := newRecFix(t)
	src := f.createWeekly(6)
	var cp Detail
	f.call("POST", "/recurring/"+src.Recurring.ID.String()+"/duplicate", "duplicateRecurring", nil, 201, &cp)
	if cp.Recurring.ID == src.Recurring.ID || cp.Recurring.Name != "Netflix (สำเนา)" || cp.Recurring.Status != "active" ||
		cp.Recurring.StartDate != f.day(0) || cp.Recurring.Progress.Total == nil || *cp.Recurring.Progress.Total != 6 ||
		cp.Recurring.Kind != KindInstallment || av(cp.Recurring.Amount) != 100 {
		t.Errorf("copy = %+v", cp.Recurring)
	}
	f.call("POST", "/recurring/"+uuid.NewString()+"/duplicate", "duplicateRecurring", nil, 404, nil)
}


func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }
