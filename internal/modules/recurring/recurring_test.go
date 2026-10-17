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
	pool := testdb.Pool(t)
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
	f.r = apitest.Engine(f.me, func(g *gin.RouterGroup) {
		g.GET("/recurring", h.List)
		g.POST("/recurring", h.Create)
		g.GET("/recurring/:id", h.Get)
		g.PUT("/recurring/:id", h.Update)
		g.DELETE("/recurring/:id", h.Delete)
		g.GET("/recurring/:id/items", h.RuleItems)
		g.POST("/recurring/:id/pause", h.Pause)
		g.POST("/recurring/:id/resume", h.Resume)
		g.POST("/recurring/:id/archive", h.Archive)
		g.POST("/recurring/:id/restore", h.Restore)
		g.POST("/recurring/:id/extend", h.Extend)
		g.GET("/recurring-items", h.Items)
		g.POST("/recurring-items", h.CreateItem)
		g.POST("/recurring-items/batch", h.Batch)
		g.GET("/recurring-items/:id", h.GetItem)
		g.PUT("/recurring-items/:id", h.UpdateItem)
		g.DELETE("/recurring-items/:id", h.DeleteItem)
		g.POST("/recurring-items/:id/create", h.CreateNow)
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

// call sends a request, checks the status and the spec, decodes into out.
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

func (f *recFix) items(ruleID uuid.UUID, tab string) []Item {
	f.t.Helper()
	var out ItemList
	f.call("GET", "/recurring/"+ruleID.String()+"/items?per_page=100&tab="+tab, "listRecurringRuleItems", nil, 200, &out)
	return out.Data
}

// A weekly bill that started two weeks ago, 6 times: the 3 dates up to today
// become pending drafts at once (one grouped notice), 3 stay upcoming.
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

func TestRecurringLifecycle(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6)
	id := d.Recurring.ID
	if d.Recurring.Counts.Pending != 3 || d.Recurring.Counts.Upcoming != 3 || d.Recurring.Progress.Total == nil ||
		*d.Recurring.Progress.Total != 6 || d.Recurring.Progress.Done != 3 {
		t.Fatalf("after create: counts %+v progress %+v", d.Recurring.Counts, d.Recurring.Progress)
	}
	if d.Recurring.NextDate == nil || *d.Recurring.NextDate != f.day(7) {
		t.Errorf("next_date = %v, want %s", d.Recurring.NextDate, f.day(7))
	}
	// One grouped notice for the 3 drafts.
	var n, count int
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*), COALESCE(MAX((payload->>'count')::int), 0) FROM notifications
		WHERE recipient_user_id = $1 AND type = 'recurring_due'`, f.me).Scan(&n, &count); err != nil {
		t.Fatal(err)
	}
	if n != 1 || count != 3 {
		t.Errorf("recurring_due notices = %d (count %d), want 1 (3)", n, count)
	}
	// The due check is idempotent.
	var list RecurringList
	f.call("GET", "/recurring", "listRecurring", nil, 200, &list)
	if list.Counts.Current != 1 || len(list.Data) != 1 || list.Data[0].Counts.Pending != 3 {
		t.Fatalf("list = %+v", list)
	}
	// The summary card: per-month equivalent (100 every 7 days = 434.82) and
	// the nearest upcoming date; q narrows the list, not the summary.
	if sm := list.Summary; sm.MonthPay != 434.82 || sm.MonthReceive != 0 || sm.Next == nil ||
		sm.Next.ID != id || sm.Next.Date != f.day(7) || sm.Next.Amount != 100 || sm.Next.Type != "expense" {
		t.Errorf("summary = %+v next %+v", sm, sm.Next)
	}
	var none RecurringList
	f.call("GET", "/recurring?q=zzz", "listRecurring", nil, 200, &none)
	if len(none.Data) != 0 || none.Summary.MonthPay != 434.82 {
		t.Errorf("q=zzz: %d rows, summary %+v", len(none.Data), none.Summary)
	}
	f.call("GET", "/recurring?q=netf", "listRecurring", nil, 200, &none)
	if len(none.Data) != 1 {
		t.Errorf("q=netf rows = %d", len(none.Data))
	}
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, nil)

	produced := f.items(id, "produced")
	if len(produced) != 3 || produced[0].Status != StatusPending || produced[0].PendingID == nil {
		t.Fatalf("produced = %+v", produced)
	}
	// The drafts carry the source chip.
	var source, ref string
	if err := f.pool.QueryRow(f.ctx, `SELECT source, source_ref::text FROM pending_transactions WHERE id = $1`,
		*produced[0].PendingID).Scan(&source, &ref); err != nil {
		t.Fatal(err)
	}
	if source != "recurring" {
		t.Errorf("draft source = %q (%s)", source, ref)
	}

	// Submitting a draft links the transaction: done; the tx shows its item.
	newest := produced[0]
	res, err := f.pending.Submit(f.ctx, f.me, pending.SubmitRequest{IDs: []uuid.UUID{*newest.PendingID}})
	if err != nil || len(res.Submitted) != 1 {
		t.Fatalf("submit: %v %+v", err, res)
	}
	var it Item
	f.call("GET", "/recurring-items/"+newest.ID.String(), "getRecurringItem", nil, 200, &it)
	if it.Status != StatusDone || it.Transaction == nil {
		t.Fatalf("after submit: %+v", it)
	}
	txID := it.Transaction.ID
	var tx transactions.TransactionDetail
	f.call("GET", "/transactions/"+txID.String(), "", nil, 200, &tx)
	if tx.RecurringItem == nil || tx.RecurringItem.ItemID != newest.ID || tx.RecurringItem.Seq == nil || *tx.RecurringItem.Seq != 3 {
		t.Errorf("tx recurring_item = %+v", tx.RecurringItem)
	}
	if f.balance() != 900 {
		t.Errorf("balance = %v, want 900", f.balance())
	}

	// Discarding a draft leaves its item "not created"; สร้างใหม่ → a real tx.
	second := produced[1]
	if _, err := f.pending.Discard(f.ctx, f.me, []uuid.UUID{*second.PendingID}); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/recurring-items/"+second.ID.String(), "", nil, 200, &it)
	if it.Status != StatusNotCreated {
		t.Fatalf("after discard: %s", it.Status)
	}
	body := map[string]any{"target": "transaction", "fields": map[string]any{"amount": 120}}
	apitest.ConformRequest(t, "createFromRecurringItem", body)
	var w ItemWriteResult
	f.call("POST", "/recurring-items/"+second.ID.String()+"/create", "createFromRecurringItem", body, 200, &w)
	if w.Item.Status != StatusDone || w.Transaction == nil || len(w.AccountBalances) != 1 || w.AccountBalances[0].Balance != 780 {
		t.Fatalf("create now: %+v", w)
	}
	f.call("POST", "/recurring-items/"+second.ID.String()+"/create", "", body, 409, nil)

	// Edit the done item and its transaction together.
	upd := map[string]any{"amount": 150, "also_update_link": true}
	apitest.ConformRequest(t, "updateRecurringItem", upd)
	var ws ItemsWriteResult
	f.call("PUT", "/recurring-items/"+newest.ID.String(), "updateRecurringItem", upd, 200, &ws)
	if len(ws.Items) != 1 || av(ws.Items[0].Amount) != 150 || !ws.Items[0].Edited || f.balance() != 730 {
		t.Fatalf("edit + link: %+v balance %v", ws, f.balance())
	}
	// Without also_update_link the transaction stays.
	f.call("PUT", "/recurring-items/"+newest.ID.String(), "updateRecurringItem", map[string]any{"amount": 99}, 200, nil)
	if f.balance() != 730 {
		t.Errorf("unlinked edit moved money: %v", f.balance())
	}
	// Edit the tx with also_update_link → the item follows.
	f.call("PUT", "/transactions/"+txID.String(), "", map[string]any{"amount": 60, "also_update_link": true}, 200, nil)
	f.call("GET", "/recurring-items/"+newest.ID.String(), "", nil, 200, &it)
	if av(it.Amount) != 60 {
		t.Errorf("item after tx edit = %v, want 60", it.Amount)
	}

	// Scope upcoming: the not-produced items take the new amount.
	f.call("PUT", "/recurring-items/"+newest.ID.String(), "updateRecurringItem",
		map[string]any{"note": "monthly plan", "scope": "upcoming"}, 200, &ws)
	if len(ws.Items) != 3 {
		t.Fatalf("scope upcoming changed %d items", len(ws.Items))
	}
	for _, u := range ws.Items {
		if u.Note == nil || *u.Note != "monthly plan" || u.Status != StatusUpcoming {
			t.Errorf("upcoming item = %+v", u)
		}
	}

	// The setting: rename + amount for upcoming items.
	put := map[string]any{"name": "Netflix HD", "amount": 200}
	apitest.ConformRequest(t, "updateRecurring", put)
	f.call("PUT", "/recurring/"+id.String(), "updateRecurring", put, 200, &d)
	if d.Recurring.Name != "Netflix HD" || av(d.Recurring.Amount) != 200 {
		t.Fatalf("update = %+v", d.Recurring)
	}
	up := f.items(id, "upcoming")
	if len(up) != 3 || av(up[0].Amount) != 200 || up[0].Description == nil || *up[0].Description != "Netflix HD" {
		t.Fatalf("upcoming after update = %+v", up)
	}

	// Pause: the upcoming items go; resume rebuilds them from today.
	f.call("POST", "/recurring/"+id.String()+"/pause", "pauseRecurring", nil, 200, &d)
	if d.Recurring.Status != "paused" || d.Recurring.Counts.Upcoming != 0 || !d.Recurring.Actions.Resume {
		t.Fatalf("pause = %+v", d.Recurring)
	var paused RecurringList
	f.call("GET", "/recurring", "listRecurring", nil, 200, &paused)
	if sm := paused.Summary; sm.MonthPay != 0 || sm.Next != nil {
		t.Errorf("paused summary = %+v, want zeros and no next", sm)
	}
	}
	f.call("POST", "/recurring/"+id.String()+"/pause", "pauseRecurring", nil, 409, nil)
	f.call("POST", "/recurring/"+id.String()+"/resume", "resumeRecurring", nil, 200, &d)
	if d.Recurring.Status != "active" || d.Recurring.Counts.Upcoming != 3 {
		t.Fatalf("resume = %+v", d.Recurring.Counts)
	}
	// Extend a count end; the ending notice re-arms.
	f.call("POST", "/recurring/"+id.String()+"/extend", "extendRecurring",
		map[string]any{"end": map[string]any{"kind": "count", "count": 8}}, 200, &d)
	if d.Recurring.Counts.Upcoming != 5 {
		t.Errorf("extend: upcoming %d, want 5", d.Recurring.Counts.Upcoming)
	}
	f.call("POST", "/recurring/"+id.String()+"/archive", "archiveRecurring", nil, 200, &d)
	f.call("POST", "/recurring/"+id.String()+"/restore", "restoreRecurring", nil, 200, &d)
	if d.Recurring.Status != "paused" {
		t.Errorf("restore → %s, want paused", d.Recurring.Status)
	}

	// Delete with the linked transactions: both txs go, the money comes back.
	var del DeleteResult
	f.call("DELETE", "/recurring/"+id.String()+"?delete_linked_txs=true", "deleteRecurring", nil, 200, &del)
	if del.DeletedTransactionCount != 2 || f.balance() != 1000 {
		t.Errorf("delete = %+v balance %v", del, f.balance())
	}
	f.call("GET", "/recurring/"+id.String(), "", nil, 404, nil)
}

func TestRecurringEndingNotice(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(5) // 3 due, 2 upcoming → ending
	if !d.Recurring.EndingSoon {
		t.Errorf("ending_soon = false with %d upcoming", d.Recurring.Counts.Upcoming)
	}
	var left int
	if err := f.pool.QueryRow(f.ctx, `SELECT (payload->>'left')::int FROM notifications
		WHERE recipient_user_id = $1 AND type = 'recurring_ending'`, f.me).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 2 {
		t.Errorf("left = %d, want 2", left)
	}
	// Told once.
	f.call("GET", "/recurring", "", nil, 200, nil)
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'recurring_ending'`, f.me).Scan(&n)
	if n != 1 {
		t.Errorf("ending notices = %d, want 1", n)
	}
}

func TestRecurringItemsAndOneOffs(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(6)

	// A future-dated transaction is an upcoming one-off, not a row.
	body := map[string]any{"type": "expense", "amount": 40, "account_id": f.wallet, "category_id": f.food,
		"date": f.day(3), "description": "Concert"}
	var wr struct {
		Rows     []json.RawMessage `json:"rows"`
		Upcoming *Item             `json:"upcoming"`
	}
	f.call("POST", "/transactions", "createTransaction", body, 201, &wr)
	if len(wr.Rows) != 0 || wr.Upcoming == nil || wr.Upcoming.Status != StatusUpcoming || wr.Upcoming.Recurring != nil {
		t.Fatalf("future create = %+v", wr)
	}
	if f.balance() != 1000 {
		t.Errorf("an upcoming item moved money: %v", f.balance())
	}
	var list ItemList
	f.call("GET", "/recurring-items?kind=one_off", "listRecurringItems", nil, 200, &list)
	if len(list.Data) != 1 || list.Data[0].ID != wr.Upcoming.ID {
		t.Fatalf("one-offs = %+v", list.Data)
	}
	f.call("GET", "/recurring-items?status=pending", "listRecurringItems", nil, 200, &list)
	if len(list.Data) != 3 {
		t.Errorf("pending items = %d, want 3", len(list.Data))
	}

	// A real transaction moved to a future date becomes upcoming.
	f.call("POST", "/transactions", "createTransaction", map[string]any{"type": "expense", "amount": 25,
		"account_id": f.wallet, "date": f.day(0)}, 201, nil)
	var txID uuid.UUID
	_ = f.pool.QueryRow(f.ctx, `SELECT id FROM transactions WHERE user_id = $1 AND amount = 25`, f.me).Scan(&txID)
	if f.balance() != 975 {
		t.Fatalf("balance = %v", f.balance())
	}
	f.call("PUT", "/transactions/"+txID.String(), "updateTransaction", map[string]any{"date": f.day(10)}, 200, &wr)
	if wr.Upcoming == nil || wr.Upcoming.Date != f.day(10) || f.balance() != 1000 {
		t.Fatalf("move to upcoming = %+v balance %v", wr, f.balance())
	}

	// A manual item under the setting, dated today → not created.
	mi := map[string]any{"recurring_id": d.Recurring.ID, "date": f.day(0), "type": "expense", "amount": 15}
	apitest.ConformRequest(t, "createRecurringItem", mi)
	var w ItemWriteResult
	f.call("POST", "/recurring-items", "createRecurringItem", mi, 201, &w)
	if !w.Item.Manual || w.Item.Status != StatusNotCreated || w.Item.Recurring == nil {
		t.Fatalf("manual item = %+v", w.Item)
	}
	f.call("POST", "/recurring-items/"+w.Item.ID.String()+"/archive", "archiveRecurringItem", nil, 200, &w)
	if w.Item.Status != StatusArchived {
		t.Errorf("archive → %s", w.Item.Status)
	}
	f.call("POST", "/recurring-items/"+w.Item.ID.String()+"/restore", "restoreRecurringItem", nil, 200, &w)

	// Batch: create (pending) the manual one, archive + delete one-offs.
	b := map[string]any{"ids": []uuid.UUID{w.Item.ID}, "action": "create", "target": "transaction"}
	apitest.ConformRequest(t, "batchRecurringItems", b)
	var br BatchResult
	f.call("POST", "/recurring-items/batch", "batchRecurringItems", b, 200, &br)
	if len(br.Items) != 1 || br.Items[0].Status != StatusDone || f.balance() != 985 {
		t.Fatalf("batch create = %+v balance %v", br, f.balance())
	}
	f.call("POST", "/recurring-items/batch", "batchRecurringItems", b, 409, nil)
	f.call("POST", "/recurring-items/batch", "batchRecurringItems",
		map[string]any{"ids": []uuid.UUID{wr.Upcoming.ID}, "action": "edit", "fields": map[string]any{"amount": 30}}, 200, &br)
	if av(br.Items[0].Amount) != 30 || !br.Items[0].Edited {
		t.Errorf("batch edit = %+v", br.Items[0])
	}
	f.call("POST", "/recurring-items/batch", "batchRecurringItems",
		map[string]any{"ids": []uuid.UUID{wr.Upcoming.ID}, "action": "delete"}, 200, &br)
	if len(br.DeletedIDs) != 1 {
		t.Errorf("batch delete = %+v", br)
	}

	// Delete an item with its transaction.
	var dr ItemDeleteResult
	f.call("DELETE", "/recurring-items/"+w.Item.ID.String()+"?delete_linked_tx=true", "deleteRecurringItem", nil, 200, &dr)
	if dr.TransactionID == nil || f.balance() != 1000 {
		t.Errorf("delete item = %+v balance %v", dr, f.balance())
	}

	// DELETE /transactions ?also_delete_link takes the item along.
	produced := f.items(d.Recurring.ID, "produced")
	res, err := f.pending.Submit(f.ctx, f.me, pending.SubmitRequest{IDs: []uuid.UUID{*produced[0].PendingID}})
	if err != nil || len(res.Submitted) != 1 {
		t.Fatalf("submit: %v", err)
	}
	var del struct {
		RecurringItemID *uuid.UUID `json:"recurring_item_id"`
	}
	f.call("DELETE", "/transactions/"+res.Submitted[0].Rows[0].ID.String()+"?also_delete_link=true", "deleteTransaction", nil, 200, &del)
	if del.RecurringItemID == nil || *del.RecurringItemID != produced[0].ID {
		t.Errorf("also_delete_link: %+v", del)
	}
	f.call("GET", "/recurring-items/"+produced[0].ID.String(), "", nil, 404, nil)
}

func TestRecurringValidation(t *testing.T) {
	f := newRecFix(t)
	bad := map[string]any{"name": "X", "type": "expense", "amount": 10, "account_id": f.wallet,
		"rule": map[string]any{"freq": "monthly"}, "start_date": f.day(0),
		"end": map[string]any{"kind": "date", "date": f.today.AddDate(3, 0, 0).Format(dateLayout)}}
	raw := f.call("POST", "/recurring", "", bad, 400, nil)
	if !contains(raw, "END_TOO_FAR") {
		t.Errorf("end too far: %s", raw)
	}
	bad["end"] = nil
	bad["rule"] = map[string]any{"freq": "hourly"}
	if raw := f.call("POST", "/recurring", "", bad, 400, nil); !contains(raw, "INVALID_RULE") {
		t.Errorf("invalid rule: %s", raw)
	}
	bad["rule"] = map[string]any{"freq": "monthly"}
	bad["category_id"] = uuid.New()
	if raw := f.call("POST", "/recurring", "", bad, 400, nil); !contains(raw, "INVALID_CATEGORY") {
		t.Errorf("invalid category: %s", raw)
	}
	// No end on a monthly setting → one year of items; dates mode = the dates.
	delete(bad, "category_id")
	var d Detail
	f.call("POST", "/recurring", "createRecurring", bad, 201, &d)
	if d.Recurring.Counts.Upcoming < 11 || d.Recurring.Progress.Total != nil {
		t.Errorf("no end monthly: %+v", d.Recurring)
	}
	f.call("POST", "/recurring/"+d.Recurring.ID.String()+"/extend", "extendRecurring", map[string]any{}, 200, &d)
	if d.Recurring.Counts.Upcoming < 23 {
		t.Errorf("extend no-end: upcoming %d", d.Recurring.Counts.Upcoming)
	}
	bad["rule"] = map[string]any{"freq": "dates", "dates": []string{f.day(5), f.day(9), f.day(5)}}
	f.call("POST", "/recurring", "createRecurring", bad, 201, &d)
	if d.Recurring.Counts.Upcoming != 2 || d.Recurring.Progress.Total == nil || *d.Recurring.Progress.Total != 2 {
		t.Errorf("dates: %+v", d.Recurring)
	}
}

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }

// Goal auto-saving: the due check makes ONE saving draft a month, on the
// setting's day, from its wallet or with none; it does not repeat in the month.
func TestGoalAutoSaveDraft(t *testing.T) {
	f := newRecFix(t)
	goal := uuid.New()
	f.exec(`INSERT INTO saving_goals (id, user_id, name, target_amount) VALUES ($1, $2, 'Japan', 80000)`, goal, f.me)
	day := f.today.Day()
	// Not yet due: a day after today (when today is not the last day), no draft.
	if day < 28 {
		if err := f.savings.SetAutoSave(f.ctx, f.me, &goal, saving_goals.AutoSaveRequest{Amount: 500, Day: day + 1}); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.DueCheck(f.ctx, f.me, ""); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM pending_transactions WHERE user_id = $1 AND source = 'auto_save'`, f.me).Scan(&n)
		if n != 0 {
			t.Fatalf("drafts before the day = %d", n)
		}
	}
	// Due today, from the wallet; and no wallet for unallocated.
	if err := f.savings.SetAutoSave(f.ctx, f.me, &goal, saving_goals.AutoSaveRequest{Amount: 500, Day: day, AccountID: &f.wallet}); err != nil {
		t.Fatal(err)
	}
	if err := f.savings.SetAutoSave(f.ctx, f.me, nil, saving_goals.AutoSaveRequest{Amount: 100, Day: day}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // the second run changes nothing
		if err := f.svc.DueCheck(f.ctx, f.me, ""); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.pool.Query(f.ctx, `SELECT kind, draft->>'goal_id', draft->>'account_id', (draft->>'amount')::float8
		FROM pending_transactions WHERE user_id = $1 AND source = 'auto_save' ORDER BY (draft->>'amount')::float8`, f.me)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type d struct {
		kind         string
		goal, wallet *string
		amount       float64
	}
	var got []d
	for rows.Next() {
		var x d
		if err := rows.Scan(&x.kind, &x.goal, &x.wallet, &x.amount); err != nil {
			t.Fatal(err)
		}
		got = append(got, x)
	}
	if len(got) != 2 || got[0].amount != 100 || got[0].goal != nil || got[0].wallet != nil ||
		got[1].amount != 500 || got[1].goal == nil || *got[1].goal != goal.String() || got[1].wallet == nil || got[0].kind != "saving" {
		t.Fatalf("drafts = %+v", got)
	}
	// The goal page shows the setting and the next date moved to next month.
	page, err := f.savings.Get(f.ctx, f.me, goal, 20)
	if err != nil || page.Goal.AutoSave == nil || page.Goal.AutoSave.Amount != 500 || page.Goal.AutoSave.Wallet == nil ||
		page.Goal.AutoSave.NextDate == nil || *page.Goal.AutoSave.NextDate <= f.day(0) {
		t.Errorf("goal auto_save = %+v (%v)", page.Goal.AutoSave, err)
	}
}

// av — a nullable amount as a number (0 = unknown).
func av(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
