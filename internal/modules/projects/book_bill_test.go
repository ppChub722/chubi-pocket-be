package projects

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

func boolp(v bool) *bool { return &v }

// newBookFixture — the quick-create fixture whose owner has the system categories (debts need them).
func newBookFixture(t *testing.T) *qcFixture {
	t.Helper()
	f := newQCFixture(t)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := categories.NewService(categories.NewStore(f.pool)).SeedForUser(ctx, tx, f.owner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *qcFixture) net(t *testing.T, key string) float64 {
	t.Helper()
	var v float64
	if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(SUM(l.signed), 0)::float8 FROM (`+
		shared.DebtLedgerSQL("$1")+`) l WHERE l.key = $2`, f.owner, key).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func conformJSON(t *testing.T, op string, status int, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	apitest.Conform(t, op, status, raw)
}

func (f *qcFixture) book(t *testing.T, req EventBillBookRequest) *EventBillBookResult {
	t.Helper()
	out, err := f.svc.BookBill(context.Background(), f.owner, req, false)
	if err != nil {
		t.Fatalf("book bill: %v", err)
	}
	conformJSON(t, "bookEventBill", 201, out)
	return out.(*EventBillBookResult)
}

// 0.4.1-1 E8: one call books the bill and my book. I paid: my tx is the whole bill (the origin);
// Bee handed her share back on the spot ("คืนแล้ว", E2: debt + repayment, her balance stays 0);
// Dan owes me his share.
func TestBookBillIPaid(t *testing.T) {
	f := newBookFixture(t)
	bee, dan := shared.DebtKey(&f.contactB, "Bee"), shared.DebtKey(nil, "Dan")
	bee0 := f.net(t, bee) // the fixture's own debts with Bee
	res := f.book(t, EventBillBookRequest{
		NewEvent: &NewEventInput{Name: "Trip"},
		Bill: EventBillInput{Kind: PTTypeExpense, Amount: 300, Date: "2026-09-10", Members: []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: 100}},
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 100}, PaidBack: true},
			{BillMember: BillMember{BillPerson: BillPerson{PersonName: "Dan"}, Share: 100}},
		}},
		MyBooks: EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal, CategoryID: &f.cat},
	})
	if got := f.accountBalance(t, f.acctPersonal); got != 800 { // −300 the bill, +100 Bee handed back
		t.Errorf("wallet = %v, want 800", got)
	}
	if n := f.net(t, bee) - bee0; n != 0 {
		t.Errorf("Bee's balance moved %v, want 0 (paid back on the spot)", n)
	}
	if n := f.net(t, dan); n != 100 {
		t.Errorf("Dan's balance = %v, want +100", n)
	}
	if res.Row == nil || !res.MyBooks.Tx || !res.MyBooks.Debt || len(res.Paybacks) != 1 || res.TransactionID == nil {
		t.Fatalf("result: row %v books %+v paybacks %d tx %v", res.Row != nil, res.MyBooks, len(res.Paybacks), res.TransactionID)
	}
	if res.Paybacks[0].Amount != 100 || len(res.Effects.Debts) != 3 || len(res.Effects.Transactions) != 2 { // my bill tx + Bee's repayment tx
		t.Errorf("payback %v, effects debts %d tx %d", res.Paybacks[0].Amount, len(res.Effects.Debts), len(res.Effects.Transactions))
	}
	var linked *uuid.UUID
	_ = f.pool.QueryRow(context.Background(), `SELECT linked_transaction_id FROM project_transactions WHERE id = $1`, res.Row.ID).Scan(&linked)
	if linked == nil || *linked != *res.TransactionID {
		t.Errorf("the bill's origin = %v, want my tx %v", linked, res.TransactionID)
	}
}

// E3 / E4 / E5 / preview: Bee paid.
func TestBookBillSomeoneElsePaid(t *testing.T) {
	f := newBookFixture(t)
	ctx := context.Background()
	bee := shared.DebtKey(&f.contactB, "Bee")
	bee0 := f.net(t, bee)
	members := func(meBack bool) []EventBillPerson {
		return []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: 100}, PaidBack: meBack},
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 200}},
		}
	}
	// E4: not paid back → my share with NO wallet (the wallet sent is ignored) + the debt to Bee
	res := f.book(t, EventBillBookRequest{
		NewEvent: &NewEventInput{Name: "Dinner"},
		Bill:     EventBillInput{Kind: PTTypeExpense, Amount: 300, Date: "2026-09-11", Payer: &BillPerson{ContactID: &f.contactB}, Members: members(false)},
		MyBooks:  EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal},
	})
	pid := res.Event.ID
	if f.accountBalance(t, f.acctPersonal) != 1000 || f.net(t, bee)-bee0 != -100 {
		t.Fatalf("E4: wallet %v Bee moved %v (want 1000 / −100)", f.accountBalance(t, f.acctPersonal), f.net(t, bee)-bee0)
	}
	var acc *uuid.UUID
	var amount float64
	_ = f.pool.QueryRow(ctx, `SELECT account_id, amount FROM transactions WHERE id = $1`, *res.TransactionID).Scan(&acc, &amount)
	if acc != nil || amount != 100 {
		t.Errorf("my share tx: wallet %v amount %v (want none / 100)", acc, amount)
	}
	// E3: I paid Bee back on the spot → my share from my wallet, no debt
	res = f.book(t, EventBillBookRequest{
		ProjectID: &pid,
		Bill:      EventBillInput{Kind: PTTypeExpense, Amount: 300, Date: "2026-09-12", Payer: &BillPerson{ContactID: &f.contactB}, Members: members(true)},
		MyBooks:   EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal},
	})
	if f.accountBalance(t, f.acctPersonal) != 900 || f.net(t, bee)-bee0 != -100 || res.MyBooks.Debt || len(res.Paybacks) != 1 {
		t.Fatalf("E3: wallet %v Bee %v books %+v paybacks %d", f.accountBalance(t, f.acctPersonal), f.net(t, bee), res.MyBooks, len(res.Paybacks))
	}
	// E5: paying Bee back 100 in the event: this event already holds my debt to her → no new debt by default
	pay := EventBillBookRequest{
		ProjectID: &pid,
		Bill:      EventBillInput{Kind: PTTypePayback, Amount: 100, Date: "2026-09-13", From: &BillPerson{Me: true}, To: &BillPerson{ContactID: &f.contactB}},
		MyBooks:   EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal},
	}
	pv, err := f.svc.BookBill(ctx, f.owner, pay, true)
	if err != nil {
		t.Fatal(err)
	}
	conformJSON(t, "bookEventBill", 200, pv)
	p := pv.(*EventBillBookPreview)
	if p.DebtDefault || len(p.People) != 1 || p.People[0].Before != bee0-100 || p.People[0].After != bee0 ||
		len(p.Wallets) != 1 || p.Wallets[0].Delta != -100 {
		t.Fatalf("preview = %+v", p)
	}
	if f.accountBalance(t, f.acctPersonal) != 900 {
		t.Fatalf("the preview saved something: wallet %v", f.accountBalance(t, f.acctPersonal))
	}
	f.book(t, pay)
	if f.accountBalance(t, f.acctPersonal) != 800 || f.net(t, bee) != bee0 {
		t.Errorf("after the payback: wallet %v Bee %v (want 800 / 0)", f.accountBalance(t, f.acctPersonal), f.net(t, bee))
	}
	// my_books on a bill I'm not in
	_, err = f.svc.BookBill(ctx, f.owner, EventBillBookRequest{ProjectID: &pid,
		Bill: EventBillInput{Kind: PTTypeExpense, Amount: 50, Date: "2026-09-14", Payer: &BillPerson{ContactID: &f.contactB}, Members: []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 50}}}},
		MyBooks: EventBillBooks{On: true, Tx: true}}, false)
	if err == nil {
		t.Errorf("my books on a bill I'm not in: no error")
	}
}

// E6 + B4: the payer books later (my tx becomes the origin, the whole bill); a bill edit keeps
// the split rows, so a copy pointing at one stays linked.
func TestRebookBill(t *testing.T) {
	f := newBookFixture(t)
	ctx := context.Background()
	res := f.book(t, EventBillBookRequest{
		NewEvent: &NewEventInput{Name: "Camp"},
		Bill: EventBillInput{Kind: PTTypeExpense, Amount: 400, Date: "2026-09-20", Members: []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: 200}},
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 200}},
		}},
		MyBooks: EventBillBooks{On: false},
	})
	billID := res.Row.ID
	if res.TransactionID != nil || res.MyBooks.Tx {
		t.Fatalf("nothing booked yet: %+v", res.MyBooks)
	}
	out, err := f.svc.RebookBill(ctx, f.owner, billID, EventBillRebookRequest{
		MyBooks: EventBillBooks{On: true, Tx: true, Debt: boolp(false), AccountID: &f.acctPersonal}}, false)
	if err != nil {
		t.Fatal(err)
	}
	conformJSON(t, "rebookEventBill", 200, out)
	r := out.(*EventBillBookResult)
	var linked *uuid.UUID
	var amount float64
	_ = f.pool.QueryRow(ctx, `SELECT pt.linked_transaction_id, t.amount FROM project_transactions pt
		JOIN transactions t ON t.id = pt.linked_transaction_id WHERE pt.id = $1`, billID).Scan(&linked, &amount)
	if r.TransactionID == nil || linked == nil || *linked != *r.TransactionID || amount != 400 || f.accountBalance(t, f.acctPersonal) != 600 {
		t.Fatalf("E6: origin %v tx %v amount %v wallet %v", linked, r.TransactionID, amount, f.accountBalance(t, f.acctPersonal))
	}

	// B4: Bee's copy points at the bill and her person; editing the shares keeps both
	beeRow := billID
	copyID := qcMustV7(t)
	if _, err := f.pool.Exec(ctx, `INSERT INTO transactions (id, user_id, type, amount, date, project_id, source_project_transaction_id)
		VALUES ($1, $2, 'expense', 200, '2026-09-20', $3, $4)`, copyID, f.friend, res.Event.ID, beeRow); err != nil {
		t.Fatal(err)
	}
	out, err = f.svc.RebookBill(ctx, f.owner, billID, EventBillRebookRequest{
		Bill: &EventBillInput{Kind: PTTypeExpense, Amount: 450, Date: "2026-09-20", Members: []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: 200}},
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 250}},
		}},
		MyBooks: EventBillBooks{On: true, Tx: true, Debt: boolp(false)}}, false)
	if err != nil {
		t.Fatalf("B4: editing a bill with a copy on a split row: %v", err)
	}
	var src *uuid.UUID
	var share float64
	_ = f.pool.QueryRow(ctx, `SELECT t.source_project_transaction_id, c.share FROM transactions t
		JOIN split_transaction_people c ON c.id = t.split_person_id WHERE t.id = $1`, copyID).Scan(&src, &share)
	if src == nil || *src != beeRow || share != 250 {
		t.Errorf("B4: Bee's copy source %v (want %v), her row %v (want 250)", src, beeRow, share)
	}
	_, _ = f.pool.Exec(ctx, `DELETE FROM transactions WHERE id = $1`, copyID)
}

// E8 close-out: un-ticking "คืนแล้ว" on a bill edit removes the payback row made for it and my
// repayment of it (its wallet tx too), in the same DB tx; re-ticking makes them again; a changed
// share remakes them at the new amount. The board shows the tick (BoardSplit.paid_back).
func TestRebookBillUntickPaidBack(t *testing.T) {
	f := newBookFixture(t)
	ctx := context.Background()
	bee := shared.DebtKey(&f.contactB, "Bee")
	bee0 := f.net(t, bee)
	bill := func(beeShare float64, back bool) EventBillInput {
		return EventBillInput{Kind: PTTypeExpense, Amount: 300, Date: "2026-09-21", Members: []EventBillPerson{
			{BillMember: BillMember{BillPerson: BillPerson{Me: true}, Share: 300 - beeShare}},
			{BillMember: BillMember{BillPerson: BillPerson{ContactID: &f.contactB}, Share: beeShare}, PaidBack: back},
		}}
	}
	books := EventBillBooks{On: true, Tx: true, AccountID: &f.acctPersonal}
	res := f.book(t, EventBillBookRequest{NewEvent: &NewEventInput{Name: "Lunch"}, Bill: bill(100, true), MyBooks: books})
	billID := res.Row.ID
	if len(res.Paybacks) != 1 || f.accountBalance(t, f.acctPersonal) != 800 || f.net(t, bee) != bee0 {
		t.Fatalf("booked: paybacks %d wallet %v Bee moved %v", len(res.Paybacks), f.accountBalance(t, f.acctPersonal), f.net(t, bee)-bee0)
	}
	if len(res.Row.Children) != 1 || !res.Row.Children[0].PaidBack {
		t.Fatalf("board tick: %+v", res.Row.Children)
	}
	payback := res.Paybacks[0].ID
	rebook := func(in EventBillInput) *EventBillBookResult {
		t.Helper()
		out, err := f.svc.RebookBill(ctx, f.owner, billID, EventBillRebookRequest{Bill: &in, MyBooks: books}, false)
		if err != nil {
			t.Fatalf("rebook: %v", err)
		}
		conformJSON(t, "rebookEventBill", 200, out)
		return out.(*EventBillBookResult)
	}

	// un-tick: the payback row and my repayment go; Bee owes me her share again; the wallet gives back the 100
	r := rebook(bill(100, false))
	if n := f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE id = $1`, payback); n != 0 {
		t.Errorf("the payback row is still there")
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1 AND kind = 'repayment'
		AND counterparty_contact_id = $2`, f.owner, f.contactB); n != 0 {
		t.Errorf("my repayment is still there (%d)", n)
	}
	if f.accountBalance(t, f.acctPersonal) != 700 || f.net(t, bee)-bee0 != 100 {
		t.Errorf("after un-tick: wallet %v (want 700) Bee moved %v (want +100)", f.accountBalance(t, f.acctPersonal), f.net(t, bee)-bee0)
	}
	if len(r.Effects.RemovedBillIDs) != 1 || r.Effects.RemovedBillIDs[0] != payback || len(r.Effects.RemovedDebtIDs) != 1 ||
		len(r.Effects.RemovedTransactionIDs) != 1 || r.Row.Children[0].PaidBack || len(r.Paybacks) != 0 {
		t.Errorf("effects removed bills %v debts %v txs %v, tick %v", r.Effects.RemovedBillIDs, r.Effects.RemovedDebtIDs,
			r.Effects.RemovedTransactionIDs, r.Row.Children[0].PaidBack)
	}

	// tick again (no duplicate on a second save), then change her share: remade at the new amount
	r = rebook(bill(100, true))
	rebook(bill(100, true))
	if n := f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE paid_back_for_id = $1`, billID); n != 1 || len(r.Paybacks) != 1 {
		t.Fatalf("re-tick: %d payback rows (want 1)", n)
	}
	r = rebook(bill(120, true))
	var amount float64
	_ = f.pool.QueryRow(ctx, `SELECT amount FROM project_transactions WHERE paid_back_for_id = $1`, billID).Scan(&amount)
	var repaid float64
	_ = f.pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0)::float8 FROM personal_debts WHERE user_id = $1 AND kind = 'repayment'
		AND counterparty_contact_id = $2`, f.owner, f.contactB).Scan(&repaid)
	// my bill debt for Bee stays at 100 until debts follow a bill edit (0.4.1-2 auto sync)
	if amount != 120 || repaid != 120 || f.accountBalance(t, f.acctPersonal) != 820 {
		t.Errorf("share change: payback %v repaid %v wallet %v (want 120 / 120 / 820)", amount, repaid, f.accountBalance(t, f.acctPersonal))
	}
}
