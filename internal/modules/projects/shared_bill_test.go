package projects

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// 0.4.1-2 shared bills.

// acceptDebts — the user's auto-accept for new split bills (owner 2026-10-28; default off).
func (f *qcFixture) acceptDebts(t *testing.T, user uuid.UUID, on bool) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO user_notification_settings (user_id, auto_accept_debts)
		VALUES ($1, $2) ON CONFLICT (user_id) DO UPDATE SET auto_accept_debts = $2`, user, on); err != nil {
		t.Fatal(err)
	}
}

// S1 / S2 / S5 / S6: a split bill with a linked contact is a shared bill; Bee's book follows it by
// her switch (auto_sync_debts), Alice's own copy always follows her save.
func TestSplitBillReachesMembers(t *testing.T) {
	f := newBookFixture(t)
	ctx := context.Background()
	f.acceptDebts(t, f.friend, true)
	books := EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal, CategoryID: &f.cat}
	split := func(amount, bee float64, payer *BillPerson) EventBillInput {
		return EventBillInput{Kind: PTTypeExpense, Amount: amount, Date: "2026-09-10", Description: strp("Dinner"),
			Payer: payer, Members: []EventBillPerson{
				{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: amount - bee}},
				{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: bee}},
			}}
	}
	res := f.book(t, EventBillBookRequest{Split: true, Bill: split(300, 100, nil), MyBooks: books})
	billID := res.Row.ID
	if res.Event.Kind != KindSplit {
		t.Fatalf("event kind = %q, want split", res.Event.Kind)
	}
	if got := f.accountBalance(t, f.acctPersonal); got != 700 {
		t.Errorf("Alice's wallet = %v, want 700 (she paid the whole bill)", got)
	}
	beeCopy := func() (amount float64, account *uuid.UUID) {
		t.Helper()
		if err := f.pool.QueryRow(ctx, `SELECT amount::float8, account_id FROM transactions
			WHERE user_id = $1 AND source_project_transaction_id = $2`, f.friend, billID).Scan(&amount, &account); err != nil {
			t.Fatalf("Bee's copy: %v", err)
		}
		return
	}
	beeDebt := func() (amount float64, dir string) {
		t.Helper()
		if err := f.pool.QueryRow(ctx, `SELECT pd.amount::float8, pd.direction FROM personal_debts pd
			JOIN personal_debt_bills b ON b.debt_id = pd.id WHERE b.user_id = $1 AND b.pt_id = $2`, f.friend, billID).Scan(&amount, &dir); err != nil {
			t.Fatalf("Bee's debt: %v", err)
		}
		return
	}
	notice := func(typ string) (n int, applied bool) {
		t.Helper()
		_ = f.pool.QueryRow(ctx, `SELECT COUNT(*), COALESCE(BOOL_OR((payload->>'applied')::boolean), FALSE) FROM notifications
			WHERE recipient_user_id = $1 AND type = $2 AND actioned_at IS NULL`, f.friend, typ).Scan(&n, &applied)
		return
	}
	// S6: Bee's switch is on (default): her copy = her share with no wallet ("จ่ายโดย Alice") + "I owe Alice".
	if a, acc := beeCopy(); a != 100 || acc != nil {
		t.Errorf("Bee's copy = %v on wallet %v, want 100 with no wallet", a, acc)
	}
	if a, dir := beeDebt(); a != 100 || dir != "received" {
		t.Errorf("Bee's debt = %v %s, want 100 received (she owes Alice)", a, dir)
	}
	if n, applied := notice("shared_bill_created"); n != 0 { // applied = info only, already actioned
		t.Errorf("Bee's open shared_bill_created = %d (applied %v), want 0 (info)", n, applied)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'shared_bill_created'`, f.friend); n != 1 {
		t.Errorf("Bee's shared_bill_created = %d, want 1", n)
	}

	// S5: Alice edits; Bee's copy + debt follow (switch on), Alice's own debt follows too.
	rebook := func(bill EventBillInput) {
		t.Helper()
		if _, err := f.svc.RebookBill(ctx, f.owner, billID, EventBillRebookRequest{Bill: &bill, MyBooks: books}, false); err != nil {
			t.Fatalf("rebook: %v", err)
		}
	}
	rebook(split(450, 150, nil))
	if a, _ := beeCopy(); a != 150 {
		t.Errorf("Bee's copy after the edit = %v, want 150", a)
	}
	if a, _ := beeDebt(); a != 150 {
		t.Errorf("Bee's debt after the edit = %v, want 150", a)
	}
	var aliceDebt float64
	if err := f.pool.QueryRow(ctx, `SELECT pd.amount::float8 FROM personal_debts pd JOIN personal_debt_bills b ON b.debt_id = pd.id
		WHERE b.user_id = $1 AND b.pt_id = $2`, f.owner, billID).Scan(&aliceDebt); err != nil || aliceDebt != 150 {
		t.Errorf("Alice's debt with Bee = %v (%v), want 150", aliceDebt, err)
	}

	// Switch off: only the notice asks; her copy keeps its values ("ไม่ตรง").
	if _, err := f.pool.Exec(ctx, `INSERT INTO user_notification_settings (user_id, auto_sync_debts) VALUES ($1, FALSE)
		ON CONFLICT (user_id) DO UPDATE SET auto_sync_debts = FALSE`, f.friend); err != nil {
		t.Fatal(err)
	}
	rebook(split(450, 120, nil))
	if a, _ := beeCopy(); a != 150 {
		t.Errorf("Bee's copy with her switch off = %v, want still 150", a)
	}
	if n, applied := notice("project_tx_changed"); n != 1 || applied {
		t.Errorf("Bee's open project_tx_changed = %d (applied %v), want 1 asking", n, applied)
	}
	rebook(split(460, 120, nil))
	if n, _ := notice("project_tx_changed"); n != 1 {
		t.Errorf("Bee's open project_tx_changed after a 2nd edit = %d, want 1 (the older one superseded)", n)
	}

	// Q-B4: the payer changes to Bee (switch on again): Bee's copy = the whole bill, Alice's = her share
	// with no wallet ("จ่ายโดย Bee"), and the debts flip.
	if _, err := f.pool.Exec(ctx, `UPDATE user_notification_settings SET auto_sync_debts = TRUE WHERE user_id = $1`, f.friend); err != nil {
		t.Fatal(err)
	}
	rebook(split(460, 120, &BillPerson{ContactID: &f.contactB}))
	if a, _ := beeCopy(); a != 460 {
		t.Errorf("Bee's copy as the payer = %v, want 460", a)
	}
	if a, dir := beeDebt(); a != 340 || dir != "paid" {
		t.Errorf("Bee's debt as the payer = %v %s, want 340 paid (Alice owes her)", a, dir)
	}
	var (
		mine    float64
		account *uuid.UUID
	)
	if err := f.pool.QueryRow(ctx, `SELECT amount::float8, account_id FROM transactions
		WHERE user_id = $1 AND source_project_transaction_id = $2`, f.owner, billID).Scan(&mine, &account); err != nil ||
		mine != 340 || account != nil {
		t.Errorf("Alice's copy = %v on wallet %v (%v), want 340 with no wallet", mine, account, err)
	}
	if got := f.accountBalance(t, f.acctPersonal); got != 1000 {
		t.Errorf("Alice's wallet = %v, want 1000 (Bee paid)", got)
	}
	var linked *uuid.UUID
	_ = f.pool.QueryRow(ctx, `SELECT linked_transaction_id FROM project_transactions WHERE id = $1`, billID).Scan(&linked)
	var beeTx uuid.UUID
	_ = f.pool.QueryRow(ctx, `SELECT id FROM transactions WHERE user_id = $1 AND source_project_transaction_id = $2`, f.friend, billID).Scan(&beeTx)
	if linked == nil || *linked != beeTx {
		t.Errorf("the bill's origin = %v, want Bee's copy %v", linked, beeTx)
	}
}

// Owner check (2026-10-27): a 900 split bill, 3 ways (me, Bee linked, Dan a name). Every member counts
// only their own share (shared.ShareAmountExpr: reports, budgets' plain branch, categories, daily);
// the rest is debts. Payer: wallet −900, share 300, Bee + Dan owe 300 each. Bee: copy 300 with no
// wallet ("จ่ายโดย Alice"), share 300, owes Alice 300. รับแทน (an income Bee received): the mirror.
func TestSplitBillCountsMyShare(t *testing.T) {
	f := newBookFixture(t)
	ctx := context.Background()
	f.acceptDebts(t, f.friend, true)
	share := func(user, bill uuid.UUID) (amount, counted float64, account *uuid.UUID) {
		t.Helper()
		if err := f.pool.QueryRow(ctx, `SELECT t.amount::float8, (`+shared.ShareAmountExpr("t")+`)::float8, t.account_id
			FROM transactions t WHERE t.user_id = $1 AND t.source_project_transaction_id = $2`, user, bill).
			Scan(&amount, &counted, &account); err != nil {
			t.Fatalf("copy of %v: %v", user, err)
		}
		return
	}
	people := func(kind string, payer *BillPerson) EventBillInput {
		return EventBillInput{Kind: kind, Amount: 900, Date: "2026-09-12", Payer: payer, Members: []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: 300}},
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 300}},
			{BillMember: BillMember{BillPerson: BillPerson{PersonName: "Dan"}, Share: 300}},
		}}
	}
	bee, dan := shared.DebtKey(&f.contactB, "Bee"), shared.DebtKey(nil, "Dan")
	bee0, dan0 := f.net(t, bee), f.net(t, dan)

	// I paid the 900 expense.
	res := f.book(t, EventBillBookRequest{Split: true, Bill: people(PTTypeExpense, nil),
		MyBooks: EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal, CategoryID: &f.cat}})
	if got := f.accountBalance(t, f.acctPersonal); got != 100 {
		t.Errorf("my wallet = %v, want 100 (1000 − 900)", got)
	}
	if a, c, _ := share(f.owner, res.Row.ID); a != 900 || c != 300 {
		t.Errorf("my tx %v counted %v, want 900 counted as my share 300", a, c)
	}
	if b, d := f.net(t, bee)-bee0, f.net(t, dan)-dan0; b != 300 || d != 300 {
		t.Errorf("owed to me: Bee %v Dan %v, want 300 each", b, d)
	}
	if a, c, acc := share(f.friend, res.Row.ID); a != 300 || c != 300 || acc != nil {
		t.Errorf("Bee's copy %v counted %v wallet %v, want 300 / 300 / no wallet", a, c, acc)
	}
	var beeOwes float64
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(SUM(l.signed), 0)::float8 FROM (`+shared.DebtLedgerSQL("$1")+`) l`,
		f.friend).Scan(&beeOwes); err != nil || beeOwes != -300 {
		t.Errorf("Bee's balance with Alice = %v (%v), want −300 (she owes)", beeOwes, err)
	}

	// รับแทน: Bee received a 900 income for the three of us.
	inc := f.book(t, EventBillBookRequest{Split: true, Bill: people(PTTypeIncome, &BillPerson{ContactID: &f.contactB}),
		MyBooks: EventBillBooks{On: true, Tx: true, CategoryID: nil}})
	if a, c, acc := share(f.owner, inc.Row.ID); a != 300 || c != 300 || acc != nil {
		t.Errorf("my income copy %v counted %v wallet %v, want 300 / 300 / no wallet", a, c, acc)
	}
	if b := f.net(t, bee) - bee0; b != 600 { // 300 from the dinner + 300 she received for me
		t.Errorf("Bee owes me %v, want 600", b)
	}
	if a, c, _ := share(f.friend, inc.Row.ID); a != 900 || c != 300 {
		t.Errorf("Bee's income copy %v counted %v, want 900 counted as her share 300", a, c)
	}
}

// SA2 / S7 / S14 / S13: the bill form's calls on a split bill. Bee's switch is off: she applies by
// hand; she disconnects; Alice deletes (Bee's records stay plain).
func TestBillFormCalls(t *testing.T) {
	f := newBookFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO user_notification_settings (user_id, auto_sync_debts) VALUES ($1, FALSE)
		ON CONFLICT (user_id) DO UPDATE SET auto_sync_debts = FALSE`, f.friend); err != nil {
		t.Fatal(err)
	}
	books := EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal, CategoryID: &f.cat}
	bill := func(amount, bee float64) EventBillInput {
		return EventBillInput{Kind: PTTypeExpense, Amount: amount, Date: "2026-09-10", Description: strp("Dinner"),
			Members: []EventBillPerson{
				{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: amount - bee}},
				{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: bee}},
			}}
	}
	res := f.book(t, EventBillBookRequest{Split: true, Bill: bill(300, 100), MyBooks: books})
	billID := res.Row.ID

	// Alice's form: payer, connected, in sync.
	view, err := f.svc.GetBill(ctx, f.owner, billID)
	if err != nil {
		t.Fatal(err)
	}
	conformJSON(t, "getEventBill", 200, view)
	if view.Event.Kind != KindSplit || view.MyCopy == nil || view.MyCopy.Role != rolePayer || !view.MyCopy.Connected ||
		view.MyCopy.InSync == nil || !*view.MyCopy.InSync || len(view.MyCopy.Debts) != 1 || !view.Rights.CanEdit {
		t.Fatalf("Alice's view: event %+v copy %+v rights %+v", view.Event, view.MyCopy, view.Rights)
	}
	// Bee's form before she applies: a share, no copy yet, a notice waits.
	bv, err := f.svc.GetBill(ctx, f.friend, billID)
	if err != nil {
		t.Fatal(err)
	}
	if bv.MyCopy == nil || bv.MyCopy.Role != roleShare || bv.MyCopy.Transaction != nil || !bv.MyCopy.PendingUpdate ||
		bv.MyCopy.PaidBy == nil || bv.MyCopy.InSync == nil || *bv.MyCopy.InSync {
		t.Fatalf("Bee's view before apply: %+v", bv.MyCopy)
	}
	// S7: she applies.
	ap, err := f.svc.ApplyBill(ctx, f.friend, billID)
	if err != nil {
		t.Fatal(err)
	}
	conformJSON(t, "applyEventBill", 200, ap)
	if ap.Bill == nil || ap.Bill.MyCopy == nil || ap.Bill.MyCopy.PendingUpdate || ap.Bill.UpdatedAt.IsZero() {
		t.Fatalf("apply answers the whole bill form (one call): %+v", ap.Bill)
	}
	if ap.MyCopy == nil || ap.MyCopy.Transaction == nil || ap.MyCopy.Transaction.Amount != 100 || ap.MyCopy.PendingUpdate ||
		!*ap.MyCopy.InSync || len(ap.Effects.Debts) != 1 || len(ap.Effects.Transactions) != 1 {
		t.Fatalf("Bee's apply: copy %+v effects debts %d tx %d", ap.MyCopy, len(ap.Effects.Debts), len(ap.Effects.Transactions))
	}
	if !ap.MyCopy.Transaction.EventBill.EditInBill || ap.MyCopy.Transaction.Project != nil {
		t.Errorf("Bee's copy: edit_in_bill %v project %v, want true / null (split)", ap.MyCopy.Transaction.EventBill.EditInBill,
			ap.MyCopy.Transaction.Project)
	}

	// S14: a stale form gets 409 with the bill now.
	stale := view.UpdatedAt.Add(-time.Second)
	b2 := bill(360, 120)
	_, err = f.svc.RebookBill(ctx, f.owner, billID, EventBillRebookRequest{Bill: &b2, MyBooks: books, ExpectedUpdatedAt: &stale}, false)
	var changed *BillChangedError
	if !errors.As(err, &changed) || changed.Bill == nil || changed.Bill.Bill.Amount != 300 {
		t.Fatalf("stale save: %v", err)
	}
	if _, err := f.svc.RebookBill(ctx, f.owner, billID, EventBillRebookRequest{Bill: &b2, MyBooks: books,
		ExpectedUpdatedAt: &view.UpdatedAt}, false); err != nil {
		t.Fatalf("fresh save: %v", err)
	}
	if bv, _ = f.svc.GetBill(ctx, f.friend, billID); !bv.MyCopy.PendingUpdate || *bv.MyCopy.InSync {
		t.Errorf("Bee after Alice's edit: pending %v in_sync %v, want true / false", bv.MyCopy.PendingUpdate, *bv.MyCopy.InSync)
	}

	// Bee disconnects: her copy + debt are plain; Alice hears it.
	dc, err := f.svc.DisconnectBill(ctx, f.friend, billID)
	if err != nil {
		t.Fatal(err)
	}
	conformJSON(t, "disconnectEventBill", 200, dc)
	if dc.MyCopy == nil || dc.MyCopy.Connected {
		t.Errorf("Bee's copy after disconnect: %+v", dc.MyCopy)
	}
	if _, err := f.svc.DisconnectBill(ctx, f.friend, billID); !errors.Is(err, ErrBillDisconnected) {
		t.Errorf("disconnect twice: %v, want BILL_DISCONNECTED", err)
	}
	if _, err := f.svc.ApplyBill(ctx, f.friend, billID); !errors.Is(err, ErrBillDisconnected) {
		t.Errorf("apply while disconnected: %v, want BILL_DISCONNECTED", err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'shared_bill_disconnected'`, f.owner); n != 1 {
		t.Errorf("Alice's shared_bill_disconnected = %d, want 1", n)
	}
	b3 := bill(400, 150)
	if _, err := f.svc.RebookBill(ctx, f.owner, billID, EventBillRebookRequest{Bill: &b3, MyBooks: books}, false); err != nil {
		t.Fatal(err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND amount = 100`, f.friend); n != 1 {
		t.Errorf("Bee's plain copy must stay 100 after a later edit (got %d rows of 100)", n)
	}

	// S13 / Q-B5: Alice deletes; the bill and its hidden split go, her records go, Bee's stay plain.
	del, err := f.svc.DeleteBill(ctx, f.owner, billID, false)
	if err != nil {
		t.Fatal(err)
	}
	conformJSON(t, "deleteEventBill", 200, del)
	if !del.RemovedProject || del.Event != nil || len(del.Effects.RemovedTransactionIDs) != 1 || len(del.Effects.RemovedDebtIDs) != 1 {
		t.Errorf("delete: %+v", del)
	}
	if got := f.accountBalance(t, f.acctPersonal); got != 1000 {
		t.Errorf("Alice's wallet after the delete = %v, want 1000", got)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND amount = 100`, f.friend); n != 1 {
		t.Errorf("Bee's plain copy must stay after the delete")
	}
	if _, err := f.svc.GetBill(ctx, f.owner, billID); !errors.Is(err, ErrPTNotFound) {
		t.Errorf("deleted bill: %v", err)
	}
}

// S9 / S11: a connected copy (transaction and debt) takes only my own fields; private_note lives
// only on shared rows.
func TestSharedCopyGuards(t *testing.T) {
	f := newBookFixture(t)
	ctx := context.Background()
	res := f.book(t, EventBillBookRequest{Split: true, Bill: EventBillInput{Kind: PTTypeExpense, Amount: 300, Date: "2026-09-10",
		Members: []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: 200}},
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 100}},
		}}, MyBooks: EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal, CategoryID: &f.cat, PrivateNote: strp("mine")}})
	copyID := *res.TransactionID
	if d, _ := f.txs.Get(ctx, f.owner, copyID); d.PrivateNote == nil || *d.PrivateNote != "mine" {
		t.Errorf("private note from the bill form = %v", d.PrivateNote)
	}
	upd := func(id uuid.UUID, body string) error {
		var req transactions.UpdateRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		_, err := f.txs.UpdateWrite(ctx, f.owner, id, req)
		return err
	}
	var fieldErr *transactions.SharedBillFieldError
	if err := upd(copyID, `{"amount": 250, "note": "x"}`); !errors.As(err, &fieldErr) || len(fieldErr.Fields) != 2 {
		t.Errorf("amount + note on my copy: %v, want SHARED_BILL_FIELD [amount note]", err)
	}
	if err := upd(copyID, `{"category_id": null, "private_note": "later"}`); err != nil {
		t.Errorf("category + private_note on my copy: %v", err)
	}
	if d, _ := f.txs.Get(ctx, f.owner, copyID); d.PrivateNote == nil || *d.PrivateNote != "later" || d.Category != nil {
		t.Errorf("after my own fields: note %v category %v", d.PrivateNote, d.Category)
	}
	if err := upd(f.bill1, `{"private_note": "no"}`); !errors.Is(err, transactions.ErrPrivateNoteNotShared) {
		t.Errorf("private_note on a plain tx: %v, want PRIVATE_NOTE_NOT_SHARED", err)
	}

	var debtID uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT debt_id FROM personal_debt_bills WHERE user_id = $1 AND pt_id = $2`,
		f.owner, res.Row.ID).Scan(&debtID); err != nil {
		t.Fatal(err)
	}
	updDebt := func(body string) error {
		var req personal_debts.UpdateEntryRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		_, err := f.svc.debts.UpdateEntry(ctx, f.owner, debtID, req)
		return err
	}
	if err := updDebt(`{"amount": 50}`); !errors.As(err, &fieldErr) {
		t.Errorf("amount on a bill debt: %v, want SHARED_BILL_FIELD", err)
	}
	if err := updDebt(`{"paid": true, "private_note": "ping Bee"}`); err != nil {
		t.Errorf("paid + private_note on a bill debt: %v", err)
	}
	var note *string
	_ = f.pool.QueryRow(ctx, `SELECT private_note FROM personal_debts WHERE id = $1`, debtID).Scan(&note)
	if note == nil || *note != "ping Bee" {
		t.Errorf("debt private note = %v", note)
	}
}

// S17: a split bill moves into an event (its people join, the copies follow, the empty split goes),
// then out again into a split bill.
func TestMoveSharedBill(t *testing.T) {
	f := newBookFixture(t)
	ctx := context.Background()
	f.acceptDebts(t, f.friend, true)
	books := EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal, CategoryID: &f.cat}
	res := f.book(t, EventBillBookRequest{Split: true, Bill: EventBillInput{Kind: PTTypeExpense, Amount: 300, Date: "2026-09-10",
		Members: []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: 200}},
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 100}},
		}}, MyBooks: books})
	billID, split := res.Row.ID, res.Event.ID
	ev, err := f.svc.Create(ctx, f.owner, CreateProjectRequest{Name: "Trip"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RebookBill(ctx, f.owner, billID, EventBillRebookRequest{MyBooks: books,
		Move: &EventBillMove{ProjectID: &split}}, false); !errors.Is(err, ErrMoveTarget) {
		t.Errorf("move to its own event: %v, want MOVE_TARGET", err)
	}
	if _, err := f.svc.RebookBill(ctx, f.owner, billID, EventBillRebookRequest{MyBooks: books,
		Move: &EventBillMove{ProjectID: &ev.Event.ID}}, false); err != nil {
		t.Fatalf("move into the event: %v", err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM projects WHERE id = $1`, split); n != 0 {
		t.Errorf("the empty split bill should go")
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND user_id = $2 AND status = 'active'`,
		ev.Event.ID, f.friend); n != 1 {
		t.Errorf("Bee should be a member of the event now")
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE source_project_transaction_id = $1 AND project_id = $2`,
		billID, ev.Event.ID); n != 2 {
		t.Errorf("both copies should follow the bill: %d", n)
	}
	view, err := f.svc.GetBill(ctx, f.friend, billID)
	if err != nil || view.Event.Kind != KindEvent || view.MyCopy == nil || view.MyCopy.InSync == nil || !*view.MyCopy.InSync {
		t.Fatalf("Bee's view after the move: %+v (%v)", view, err)
	}
	// out of the event, into a split bill
	if _, err := f.svc.RebookBill(ctx, f.owner, billID, EventBillRebookRequest{MyBooks: books,
		Move: &EventBillMove{Split: true}}, false); err != nil {
		t.Fatalf("move out into a split: %v", err)
	}
	if view, err = f.svc.GetBill(ctx, f.owner, billID); err != nil || view.Event.Kind != KindSplit {
		t.Fatalf("after moving out: %+v (%v)", view, err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE project_id = $1`, ev.Event.ID); n != 0 {
		t.Errorf("the event still holds %d rows", n)
	}
}

// Multi-payer (owner 2026-10-28): a 900 split bill, Alice paid 600 and Bee 300, shares 300 each
// (Alice, Bee, Dan). Pairs: Bee owes Alice 200, Alice owes Bee 100 (net Bee owes Alice 100), Dan owes
// Alice 200 and Bee 100. Each wallet moves what its owner paid; reports count each share.
func TestMultiPayerCopies(t *testing.T) {
	f := newBookFixture(t)
	ctx := context.Background()
	f.acceptDebts(t, f.friend, true)
	bee, dan := shared.DebtKey(&f.contactB, "Bee"), shared.DebtKey(nil, "Dan")
	bee0, dan0 := f.net(t, bee), f.net(t, dan)
	if _, err := f.svc.BookBill(ctx, f.owner, EventBillBookRequest{Split: true, Bill: EventBillInput{Kind: PTTypeExpense, Amount: 900,
		Date: "2026-09-12", Payers: []BillPayerInput{{BillPerson: BillPerson{Me: true}, Amount: 600},
			{BillPerson: BillPerson{ContactID: &f.contactB}, Amount: 200}}}, MyBooks: EventBillBooks{On: true, Tx: true}}, false); !errors.Is(err, ErrPayersSum) {
		t.Errorf("payers 800 of 900: %v, want PAYERS_SUM", err)
	}
	res := f.book(t, EventBillBookRequest{Split: true, Bill: EventBillInput{Kind: PTTypeExpense, Amount: 900, Date: "2026-09-12",
		Payers: []BillPayerInput{{BillPerson: BillPerson{Me: true}, Amount: 600}, {BillPerson: BillPerson{ContactID: &f.contactB}, Amount: 300}},
		Members: []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: 300}},
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 300}},
			{BillMember: BillMember{BillPerson: BillPerson{PersonName: "Dan"}, Share: 300}},
		}}, MyBooks: EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal, CategoryID: &f.cat}})
	copyOf := func(user uuid.UUID) (amount, counted float64) {
		t.Helper()
		if err := f.pool.QueryRow(ctx, `SELECT t.amount::float8, (`+shared.ShareAmountExpr("t")+`)::float8
			FROM transactions t WHERE t.user_id = $1 AND t.source_project_transaction_id = $2`, user, res.Row.ID).Scan(&amount, &counted); err != nil {
			t.Fatalf("copy of %v: %v", user, err)
		}
		return
	}
	if got := f.accountBalance(t, f.acctPersonal); got != 400 {
		t.Errorf("Alice's wallet = %v, want 400 (she paid 600)", got)
	}
	if a, c := copyOf(f.owner); a != 600 || c != 300 {
		t.Errorf("Alice's copy %v counted %v, want 600 / 300", a, c)
	}
	if a, c := copyOf(f.friend); a != 300 || c != 300 {
		t.Errorf("Bee's copy %v counted %v, want 300 / 300", a, c)
	}
	if b, d := f.net(t, bee)-bee0, f.net(t, dan)-dan0; b != 100 || d != 200 {
		t.Errorf("owed to Alice: Bee %v Dan %v, want 100 / 200", b, d)
	}
	var beeNet float64
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(SUM(l.signed), 0)::float8 FROM (`+shared.DebtLedgerSQL("$1")+`) l`,
		f.friend).Scan(&beeNet); err != nil || beeNet != 0 { // owes Alice 100, Dan owes her 100
		t.Errorf("Bee's book net = %v (%v), want 0 (−100 Alice, +100 Dan)", beeNet, err)
	}
	view, err := f.svc.GetBill(ctx, f.friend, res.Row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c := view.MyCopy; c == nil || c.MyPaid != 300 || c.Role != rolePayer || !*c.InSync || len(c.PaidByParts) != 1 {
		t.Errorf("Bee's copy view: %+v", c)
	}
}

// "คืนแล้ว" seen from a REMOTE payer's book (E2 for everyone) and reconnecting a disconnected copy.
// Bee paid 300; Alice's share 100, paid back on the spot: Alice's copy is 100 from her wallet with no
// debt (E3); Bee's book holds "Alice owes 100" + the repayment (net 0). Alice disconnects, then
// connects again: the same transaction takes the link back, in sync.
func TestPaidBackRemotePayerAndReconnect(t *testing.T) {
	f := newBookFixture(t)
	ctx := context.Background()
	f.acceptDebts(t, f.friend, true)
	books := EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal, CategoryID: &f.cat}
	res := f.book(t, EventBillBookRequest{Split: true, Bill: EventBillInput{Kind: PTTypeExpense, Amount: 300, Date: "2026-09-10",
		Payer: &BillPerson{ContactID: &f.contactB}, Members: []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: 100}, PaidBack: true},
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 200}},
		}}, MyBooks: books})
	billID, myTx := res.Row.ID, *res.TransactionID
	if got := f.accountBalance(t, f.acctPersonal); got != 900 {
		t.Errorf("Alice's wallet = %v, want 900 (she paid Bee back 100)", got)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debt_bills WHERE user_id = $1 AND pt_id = $2`, f.owner, billID); n != 0 {
		t.Errorf("Alice paid back: %d bill debts, want 0", n)
	}
	var beeNet float64
	var beeRows int
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(SUM(l.signed), 0)::float8, COUNT(*) FROM (`+shared.DebtLedgerSQL("$1")+`) l`,
		f.friend).Scan(&beeNet, &beeRows); err != nil || beeNet != 0 || beeRows != 2 {
		t.Errorf("Bee's book: net %v over %d rows (%v), want 0 over 2 (the debt + its repayment)", beeNet, beeRows, err)
	}

	if _, err := f.svc.DisconnectBill(ctx, f.owner, billID); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.svc.GetBill(ctx, f.owner, billID); v.MyCopy == nil || v.MyCopy.Connected {
		t.Fatalf("after disconnect: %+v", v.MyCopy)
	}
	if _, err := f.svc.RebookBill(ctx, f.owner, billID, EventBillRebookRequest{MyBooks: books}, false); err != nil {
		t.Fatalf("connect again: %v", err)
	}
	v, err := f.svc.GetBill(ctx, f.owner, billID)
	if err != nil {
		t.Fatal(err)
	}
	if c := v.MyCopy; c == nil || !c.Connected || c.Transaction == nil || c.Transaction.ID != myTx || c.InSync == nil || !*c.InSync {
		t.Errorf("after connecting again: %+v", c)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND (source_project_transaction_id = $2
		OR unlinked_project_transaction_id = $2)`, f.owner, billID); n != 1 {
		t.Errorf("Alice holds %d transactions of the bill, want 1 (the same one, relinked)", n)
	}
	if got := f.accountBalance(t, f.acctPersonal); got != 900 {
		t.Errorf("Alice's wallet after reconnect = %v, want 900", got)
	}
}

// X8: a member who left (or was removed) hears nothing more about the event's bills.
func TestBillNoticesSkipLeftMembers(t *testing.T) {
	f := newBookFixture(t)
	ctx := context.Background()
	res := f.book(t, EventBillBookRequest{
		NewEvent: &NewEventInput{Name: "Trip"},
		Bill: EventBillInput{Kind: PTTypeExpense, Amount: 300, Date: "2026-09-10", Members: []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: 200}},
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 100}},
		}},
		MyBooks: EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal, CategoryID: &f.cat},
	})
	notices := func() int {
		return f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'project_tx_changed'`, f.friend)
	}
	f.insertCopy(t, f.friend, res.Event.ID, res.Row.ID, 100) // Bee's copy of her share
	edit := func(amount float64, withBee bool) {
		t.Helper()
		members := []EventBillPerson{{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: amount - 100}}}
		if withBee {
			members = append(members, EventBillPerson{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 100}})
		}
		bill := EventBillInput{Kind: PTTypeExpense, Amount: amount, Date: "2026-09-10", Members: members}
		if _, err := f.svc.RebookBill(ctx, f.owner, res.Row.ID, EventBillRebookRequest{Bill: &bill,
			MyBooks: EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal, CategoryID: &f.cat}}, false); err != nil {
			t.Fatalf("rebook %v: %v", amount, err)
		}
	}
	edit(400, true)
	if n := notices(); n != 1 {
		t.Fatalf("Bee (active) got %d project_tx_changed, want 1", n)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE project_members SET status = 'left', left_at = NOW()
		WHERE project_id = $1 AND user_id = $2`, res.Event.ID, f.friend); err != nil {
		t.Fatal(err)
	}
	edit(500, false) // she still holds a copy of the bill
	if n := notices(); n != 1 {
		t.Errorf("Bee (left) got %d project_tx_changed, want still 1", n)
	}
}
