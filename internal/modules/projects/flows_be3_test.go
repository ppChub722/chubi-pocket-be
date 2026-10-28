package projects

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Cross-area flows (0.4.1-2 plan §1 "Shared bills" + "Added 2026-10-28" auto-accept), end to end
// through the /event-bills routes. A = Alice (the fixture owner, the payer), B = Bee (a user, linked
// through Alice's contact), C = a name only. Money: A pays 900, A / B / C share 300 each.

type flowEnv struct {
	*qcFixture
	t      *testing.T
	wallet uuid.UUID
	a, b   evClient
}

func flowEngine(f *qcFixture, as uuid.UUID) http.Handler {
	h := NewHandler(f.svc)
	return apitest.CountedEngine(as, func(g *gin.RouterGroup) {
		g.POST("/event-bills", h.BookBill)
		g.GET("/event-bills/:pt_id", h.GetBill)
		g.PUT("/event-bills/:pt_id", h.RebookBill)
		g.DELETE("/event-bills/:pt_id", h.DeleteBill)
		g.POST("/event-bills/:pt_id/apply", h.ApplyBill)
		g.POST("/event-bills/:pt_id/disconnect", h.DisconnectBill)
	})
}

func newFlowEnv(t *testing.T) *flowEnv {
	t.Helper()
	f := newBookFixture(t)
	wallet := f.newAccount(t, f.owner, "Flow wallet", 10000)
	f.addAccountMember(t, wallet, f.owner, "owner")
	return &flowEnv{qcFixture: f, t: t, wallet: wallet,
		a: evClient{t, flowEngine(f, f.owner)}, b: evClient{t, flowEngine(f, f.friend)}}
}

// bill — A paid, A / B / C share it equally.
func (e *flowEnv) bill(amount float64, desc string) map[string]any {
	s := amount / 3
	return map[string]any{"kind": "expense", "amount": amount, "date": "2026-10-05", "description": desc,
		"members": []map[string]any{
			{"me": true, "share": s},
			{"contact_id": e.contactB, "share": s},
			{"person_name": "C", "share": s},
		}}
}

func (e *flowEnv) myBooks() map[string]any {
	return map[string]any{"on": true, "tx": true, "account_id": e.wallet, "category_id": e.cat}
}

// book — POST /event-bills as A; target = {"split": true} / {"new_event": …} / {"project_id": …}.
func (e *flowEnv) book(target map[string]any, amount float64, desc string) (res map[string]any, bill, myTx uuid.UUID) {
	e.t.Helper()
	body := map[string]any{"bill": e.bill(amount, desc), "my_books": e.myBooks()}
	for k, v := range target {
		body[k] = v
	}
	res = e.a.do("POST", "/event-bills", body, "bookEventBill", http.StatusCreated)
	bill = uuid.MustParse(m(res, "row")["id"].(string))
	if s, ok := res["transaction_id"].(string); ok {
		myTx = uuid.MustParse(s)
	}
	return
}

func (e *flowEnv) edit(bill uuid.UUID, amount float64, desc string) map[string]any {
	e.t.Helper()
	return e.a.do("PUT", "/event-bills/"+bill.String(), map[string]any{"bill": e.bill(amount, desc), "my_books": e.myBooks()},
		"rebookEventBill", http.StatusOK)
}

func (e *flowEnv) view(c evClient, bill uuid.UUID) map[string]any {
	e.t.Helper()
	return c.do("GET", "/event-bills/"+bill.String(), nil, "getEventBill", http.StatusOK)
}

// setting — one of B's switches in user_notification_settings.
func (e *flowEnv) setting(user uuid.UUID, column string, on bool) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO user_notification_settings (user_id, `+column+`) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET `+column+` = $2`, user, on); err != nil {
		e.t.Fatal(err)
	}
}

// copyOf — the user's CONNECTED copy of the bill (ok false: none).
func (e *flowEnv) copyOf(user, bill uuid.UUID) (amount float64, account *uuid.UUID, ok bool) {
	e.t.Helper()
	err := e.pool.QueryRow(context.Background(), `SELECT amount::float8, account_id FROM transactions
		WHERE user_id = $1 AND source_project_transaction_id = $2`, user, bill).Scan(&amount, &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, false
	}
	if err != nil {
		e.t.Fatalf("copy of %v: %v", user, err)
	}
	return amount, account, true
}

// debtOf — the user's ONE debt connected to the bill (ok false: none).
func (e *flowEnv) debtOf(user, bill uuid.UUID) (amount float64, dir string, ok bool) {
	e.t.Helper()
	err := e.pool.QueryRow(context.Background(), `SELECT pd.amount::float8, pd.direction FROM personal_debts pd
		JOIN personal_debt_bills b ON b.debt_id = pd.id WHERE b.user_id = $1 AND b.pt_id = $2`, user, bill).Scan(&amount, &dir)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", false
	}
	if err != nil {
		e.t.Fatalf("debt of %v: %v", user, err)
	}
	return amount, dir, true
}

// notices — the user's notices of a type about the bill: all of them, and the ones still asking
// (unanswered, not applied by the switch).
func (e *flowEnv) notices(user uuid.UUID, typ string, bill uuid.UUID) (all, asking int) {
	e.t.Helper()
	if err := e.pool.QueryRow(context.Background(), `SELECT COUNT(*), COUNT(*) FILTER (WHERE actioned_at IS NULL
			AND dismissed_at IS NULL AND NOT COALESCE((payload->>'applied')::boolean, FALSE))
		FROM notifications WHERE recipient_user_id = $1 AND type = $2 AND payload->>'project_transaction_id' = $3`,
		user, typ, bill.String()).Scan(&all, &asking); err != nil {
		e.t.Fatal(err)
	}
	return
}

// TestFlowSharedSplitBill — a split bill with a linked contact (B) and a name (C), from create to
// delete: auto-accept off (notice) / on (copy at once), an edit by auto-sync on / off ("ไม่ตรง" +
// apply), B disconnects, A deletes (Q-B5); and a delete while B is still connected.
func TestFlowSharedSplitBill(t *testing.T) {
	e := newFlowEnv(t)
	ctx := context.Background()
	beeKey, cKey := shared.DebtKey(&e.contactB, "Bee"), shared.DebtKey(nil, "C")
	bee0 := e.net(t, beeKey) // the fixture's own debts with Bee

	// 1. B's auto_accept_debts is off by default: the new bill reaches B as a notice only.
	res, bill1, tx1 := e.book(map[string]any{"split": true}, 900, "Dinner")
	if k := m(res, "event")["kind"]; k != KindSplit {
		t.Fatalf("event kind = %v, want split", k)
	}
	d, err := e.txs.Get(ctx, e.owner, tx1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Amount != 900 || d.MyShare == nil || *d.MyShare != 300 {
		t.Errorf("A's tx = %v my_share %v, want 900 / 300", d.Amount, d.MyShare)
	}
	if got := e.accountBalance(t, e.wallet); got != 9100 {
		t.Errorf("A's wallet = %v, want 9100", got)
	}
	if b, c := e.net(t, beeKey)-bee0, e.net(t, cKey); b != 300 || c != 300 {
		t.Errorf("owed to A: B %v C %v, want 300 each", b, c)
	}
	if _, _, ok := e.copyOf(e.friend, bill1); ok {
		t.Errorf("B got a copy with auto-accept off")
	}
	if n := e.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1`, e.friend); n != 0 {
		t.Errorf("B's book holds %d debts, want 0 (not accepted yet)", n)
	}
	if all, asking := e.notices(e.friend, "shared_bill_created", bill1); all != 1 || asking != 1 {
		t.Errorf("B's shared_bill_created = %d (asking %d), want 1 asking", all, asking)
	}
	mc := m(e.view(e.b, bill1), "my_copy")
	if mc["role"] != roleShare || mc["transaction"] != nil || mc["pending_update"] != true || mc["in_sync"] != false {
		t.Errorf("B's view before accepting: %v", mc)
	}

	// B adds it from the notice ("เพิ่มในสมุด" = apply): her share with no wallet + "B owes A 300".
	e.b.do("POST", "/event-bills/"+bill1.String()+"/apply", nil, "applyEventBill", http.StatusOK)
	if a, acc, ok := e.copyOf(e.friend, bill1); !ok || a != 300 || acc != nil {
		t.Errorf("B's copy of bill 1 = %v on %v (%v), want 300 with no wallet", a, acc, ok)
	}
	if a, dir, ok := e.debtOf(e.friend, bill1); !ok || a != 300 || dir != "received" {
		t.Errorf("B's debt of bill 1 = %v %s (%v), want 300 received (B owes A)", a, dir, ok)
	}
	if _, asking := e.notices(e.friend, "shared_bill_created", bill1); asking != 0 {
		t.Errorf("B's notice still asks after apply")
	}

	// 2. B turns auto-accept on: the next bill is a copy at once (the notice is info).
	e.setting(e.friend, "auto_accept_debts", true)
	_, bill2, tx2 := e.book(map[string]any{"split": true}, 900, "Lunch")
	if a, acc, ok := e.copyOf(e.friend, bill2); !ok || a != 300 || acc != nil {
		t.Errorf("B's copy of bill 2 = %v on %v (%v), want 300 with no wallet", a, acc, ok)
	}
	if a, dir, ok := e.debtOf(e.friend, bill2); !ok || a != 300 || dir != "received" {
		t.Errorf("B's debt of bill 2 = %v %s (%v), want 300 received", a, dir, ok)
	}
	if all, asking := e.notices(e.friend, "shared_bill_created", bill2); all != 1 || asking != 0 {
		t.Errorf("B's shared_bill_created of bill 2 = %d (asking %d), want 1 info", all, asking)
	}

	// 3. A edits 900 → 1200 (400 each); B's auto_sync_debts is on (default): her copy + debt follow.
	e.edit(bill2, 1200, "Lunch")
	if d, err := e.txs.Get(ctx, e.owner, tx2); err != nil || d.Amount != 1200 {
		t.Errorf("A's tx after the edit = %v (%v), want 1200", d, err)
	}
	if got := e.accountBalance(t, e.wallet); got != 7900 {
		t.Errorf("A's wallet = %v, want 7900 (10000 − 900 − 1200)", got)
	}
	if a, _, _ := e.copyOf(e.friend, bill2); a != 400 {
		t.Errorf("B's copy after the edit = %v, want 400", a)
	}
	if a, _, _ := e.debtOf(e.friend, bill2); a != 400 {
		t.Errorf("B's debt after the edit = %v, want 400", a)
	}
	if _, asking := e.notices(e.friend, "project_tx_changed", bill2); asking != 0 {
		t.Errorf("B's project_tx_changed asks with her switch on")
	}

	// 4. B's auto-sync off: A edits to 1500 (500 each); B's copy keeps 400 ("ไม่ตรง"), a notice asks.
	e.setting(e.friend, "auto_sync_debts", false)
	e.edit(bill2, 1500, "Lunch")
	if a, _, _ := e.copyOf(e.friend, bill2); a != 400 {
		t.Errorf("B's copy with her switch off = %v, want still 400", a)
	}
	if a, _, _ := e.debtOf(e.friend, bill2); a != 400 {
		t.Errorf("B's debt with her switch off = %v, want still 400", a)
	}
	if _, asking := e.notices(e.friend, "project_tx_changed", bill2); asking != 1 {
		t.Errorf("B's asking project_tx_changed = %d, want 1", asking)
	}
	mc = m(e.view(e.b, bill2), "my_copy")
	if mc["connected"] != true || mc["in_sync"] != false || mc["pending_update"] != true || len(list(mc, "differences")) == 0 {
		t.Errorf("B's view out of sync: %v", mc)
	}

	// 5. B applies ("อัปเดตตาม"): her copy + debt match the bill.
	ap := e.b.do("POST", "/event-bills/"+bill2.String()+"/apply", nil, "applyEventBill", http.StatusOK)
	if mc := m(ap, "my_copy"); mc["in_sync"] != true || mc["pending_update"] != false {
		t.Errorf("B's copy after apply: %v", mc)
	}
	if a, _, _ := e.copyOf(e.friend, bill2); a != 500 {
		t.Errorf("B's copy after apply = %v, want 500", a)
	}
	if a, _, _ := e.debtOf(e.friend, bill2); a != 500 {
		t.Errorf("B's debt after apply = %v, want 500", a)
	}
	if _, asking := e.notices(e.friend, "project_tx_changed", bill2); asking != 0 {
		t.Errorf("B's notice still asks after apply")
	}

	// 6. B disconnects: her copy + debt are plain records; A hears; later edits no longer reach her.
	dc := e.b.do("POST", "/event-bills/"+bill2.String()+"/disconnect", nil, "disconnectEventBill", http.StatusOK)
	if m(dc, "my_copy")["connected"] != false {
		t.Errorf("B's copy after disconnect: %v", m(dc, "my_copy"))
	}
	if _, _, ok := e.copyOf(e.friend, bill2); ok {
		t.Errorf("B's copy still connected after disconnect")
	}
	if n := e.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND amount = 500 AND project_id IS NULL
		AND source_project_transaction_id IS NULL AND unlinked_project_transaction_id = $2`, e.friend, bill2); n != 1 {
		t.Errorf("B's plain copy (500, remembers the bill) = %d, want 1", n)
	}
	if _, _, ok := e.debtOf(e.friend, bill2); ok {
		t.Errorf("B's debt still connected after disconnect")
	}
	if n := e.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1 AND amount = 500 AND project_id IS NULL
		AND source_project_transaction_id IS NULL`, e.friend); n != 1 {
		t.Errorf("B's plain debt (500) = %d, want 1", n)
	}
	if all, _ := e.notices(e.owner, "shared_bill_disconnected", bill2); all != 1 {
		t.Errorf("A's shared_bill_disconnected = %d, want 1", all)
	}
	e.setting(e.friend, "auto_sync_debts", true)
	e.edit(bill2, 1800, "Lunch")
	if n := e.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND amount = 500`, e.friend); n != 1 {
		t.Errorf("B's plain copy must stay 500 after a later edit")
	}
	if _, _, ok := e.copyOf(e.friend, bill2); ok {
		t.Errorf("a later edit re-made B's copy after she disconnected")
	}

	// 7. A deletes bill 2 (Q-B5): gone for A (tx, debts, wallet back); B's disconnected copy stays.
	del := e.a.do("DELETE", "/event-bills/"+bill2.String(), nil, "deleteEventBill", http.StatusOK)
	if del["removed_project"] != true {
		t.Errorf("delete bill 2: removed_project = %v, want true", del["removed_project"])
	}
	if n := e.countRows(t, `SELECT COUNT(*) FROM transactions WHERE id = $1`, tx2); n != 0 {
		t.Errorf("A's tx of bill 2 survived the delete")
	}
	if n := e.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE id = $1`, bill2); n != 0 {
		t.Errorf("bill 2 survived the delete")
	}
	if got := e.accountBalance(t, e.wallet); got != 9100 {
		t.Errorf("A's wallet after deleting bill 2 = %v, want 9100", got)
	}
	if b, c := e.net(t, beeKey)-bee0, e.net(t, cKey); b != 300 || c != 300 {
		t.Errorf("owed to A after deleting bill 2: B %v C %v, want 300 each (bill 1 only)", b, c)
	}
	if n := e.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND amount = 500`, e.friend); n != 1 {
		t.Errorf("B's plain copy must stay after A's delete")
	}
	if n := e.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1 AND amount = 500`, e.friend); n != 1 {
		t.Errorf("B's plain debt must stay after A's delete")
	}
	if all, _ := e.notices(e.friend, "shared_bill_deleted", bill2); all != 0 {
		t.Errorf("B (disconnected) got %d shared_bill_deleted, want 0", all)
	}
	e.a.code("GET", "/event-bills/"+bill2.String(), nil, http.StatusNotFound, "NOT_FOUND")

	// 8. A deletes bill 1 while B is still connected: B's copy + debt stay as plain records (rule f).
	e.a.do("DELETE", "/event-bills/"+bill1.String(), nil, "deleteEventBill", http.StatusOK)
	if n := e.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND amount = 300 AND project_id IS NULL
		AND source_project_transaction_id IS NULL`, e.friend); n != 1 {
		t.Errorf("B's copy of bill 1 kept plain = %d, want 1", n)
	}
	if n := e.countRows(t, `SELECT COUNT(*) FROM personal_debts pd WHERE pd.user_id = $1 AND pd.amount = 300
		AND pd.direction = 'received' AND pd.project_id IS NULL AND pd.source_project_transaction_id IS NULL
		AND NOT EXISTS (SELECT 1 FROM personal_debt_bills b WHERE b.debt_id = pd.id)`, e.friend); n != 1 {
		t.Errorf("B's debt of bill 1 kept plain = %d, want 1", n)
	}
	if all, asking := e.notices(e.friend, "shared_bill_deleted", bill1); all != 1 || asking != 0 {
		t.Errorf("B's shared_bill_deleted of bill 1 = %d (asking %d), want 1 info", all, asking)
	}
	if got := e.accountBalance(t, e.wallet); got != 10000 {
		t.Errorf("A's wallet after deleting both = %v, want 10000", got)
	}
	if b, c := e.net(t, beeKey)-bee0, e.net(t, cKey); b != 0 || c != 0 {
		t.Errorf("owed to A after deleting both: B %v C %v, want 0", b, c)
	}
	if n := e.countRows(t, `SELECT COUNT(*) FROM projects WHERE owner_user_id = $1 AND kind = 'split'`, e.owner); n != 0 {
		t.Errorf("hidden split events left: %d", n)
	}
}

// TestFlowSplitBillEditBeforeAccept — B (auto-accept off) has not answered the new bill's notice
// when A edits it; B's auto_sync_debts is on (default).
func TestFlowSplitBillEditBeforeAccept(t *testing.T) {
	e := newFlowEnv(t)
	_, bill, _ := e.book(map[string]any{"split": true}, 900, "Dinner")
	if _, _, ok := e.copyOf(e.friend, bill); ok {
		t.Fatalf("B got a copy with auto-accept off")
	}
	e.edit(bill, 1200, "Dinner")
	// Plan (Added 2026-10-28): a new bill becomes a copy only by auto-accept or B's "เพิ่มในสมุด";
	// auto-sync is for later edits of a copy B already holds.
	if a, _, ok := e.copyOf(e.friend, bill); ok {
		t.Errorf("an edit wrote B's copy (%v) before B accepted the bill", a)
	}
	if _, _, ok := e.debtOf(e.friend, bill); ok {
		t.Errorf("an edit wrote B's debt before B accepted the bill")
	}
}

// TestFlowEventBillAutoAccept — an event with A (owner) and B (a linked member): B's
// auto_accept_events is on by default (a new bill = a copy at once); off → a notice only, which B
// answers; later edits still follow auto_sync_events.
func TestFlowEventBillAutoAccept(t *testing.T) {
	e := newFlowEnv(t)
	res, bill1, _ := e.book(map[string]any{"new_event": map[string]any{"name": "Trip"}}, 900, "Hotel")
	ev := m(res, "event")
	if ev["kind"] != KindEvent {
		t.Fatalf("event kind = %v", ev["kind"])
	}
	evID := uuid.MustParse(ev["id"].(string))
	if n := e.countRows(t, `SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND user_id = $2 AND status = 'active'`,
		evID, e.friend); n != 1 {
		t.Fatalf("B is not an active member of the event")
	}
	if a, acc, ok := e.copyOf(e.friend, bill1); !ok || a != 300 || acc != nil {
		t.Errorf("B's copy (auto-accept on by default) = %v on %v (%v), want 300 with no wallet", a, acc, ok)
	}
	if a, dir, ok := e.debtOf(e.friend, bill1); !ok || a != 300 || dir != "received" {
		t.Errorf("B's debt = %v %s (%v), want 300 received", a, dir, ok)
	}
	if all, asking := e.notices(e.friend, "shared_bill_created", bill1); all != 1 || asking != 0 {
		t.Errorf("B's shared_bill_created = %d (asking %d), want 1 info", all, asking)
	}

	// auto-accept off: the next bill is a notice only
	e.setting(e.friend, "auto_accept_events", false)
	_, bill2, _ := e.book(map[string]any{"project_id": evID}, 900, "Dinner")
	if _, _, ok := e.copyOf(e.friend, bill2); ok {
		t.Errorf("B got a copy of bill 2 with auto-accept off")
	}
	if _, _, ok := e.debtOf(e.friend, bill2); ok {
		t.Errorf("B got a debt of bill 2 with auto-accept off")
	}
	if all, asking := e.notices(e.friend, "shared_bill_created", bill2); all != 1 || asking != 1 {
		t.Errorf("B's shared_bill_created of bill 2 = %d (asking %d), want 1 asking", all, asking)
	}
	if mc := m(e.view(e.b, bill2), "my_copy"); mc["transaction"] != nil || mc["pending_update"] != true {
		t.Errorf("B's view of bill 2: %v", mc)
	}

	// B adds it from the notice
	e.b.do("POST", "/event-bills/"+bill2.String()+"/apply", nil, "applyEventBill", http.StatusOK)
	if a, _, ok := e.copyOf(e.friend, bill2); !ok || a != 300 {
		t.Errorf("B's copy of bill 2 after apply = %v (%v), want 300", a, ok)
	}
	if _, asking := e.notices(e.friend, "shared_bill_created", bill2); asking != 0 {
		t.Errorf("B's notice still asks after apply")
	}

	// auto-accept is only for NEW bills: an edit follows auto_sync_events (on by default)
	e.edit(bill2, 1200, "Dinner")
	if a, _, _ := e.copyOf(e.friend, bill2); a != 400 {
		t.Errorf("B's copy after the edit = %v, want 400 (auto-sync on)", a)
	}
	if a, _, _ := e.debtOf(e.friend, bill2); a != 400 {
		t.Errorf("B's debt after the edit = %v, want 400", a)
	}
}
