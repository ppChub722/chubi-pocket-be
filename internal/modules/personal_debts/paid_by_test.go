package personal_debts

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

func f64(v float64) *float64 { return &v }

// TestPaidByLeeScenario — bill 123, Lee paid 13, I paid 110: ONE expense of 123 (my consumption),
// the wallet moves 110, and the debt "I owe Lee 13" comes with the same save. Edits move the cash
// part and the debt together; the debt can't be deleted on its own.
func TestPaidByLeeScenario(t *testing.T) {
	f := newDebtHFix(t)
	w, err := f.txs.CreateWrite(f.ctx, f.me, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &f.wallet, Amount: 123, Date: "2026-10-12",
		Description: testdb.Str("Dinner"),
		PaidBy:      &transactions.PaidByInput{PersonName: "Lee", Amount: 13},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	row := w.Rows[0]
	if row.Amount != 123 || row.WalletAmount != 110 || row.LinkMode != transactions.LinkPaid ||
		row.MyShare == nil || *row.MyShare != 123 || row.Account == nil {
		t.Fatalf("row = amount %v wallet %v mode %s share %v account %v", row.Amount, row.WalletAmount, row.LinkMode, row.MyShare, row.Account)
	}
	pb := row.PaidBy
	if pb == nil || pb.Amount != 13 || pb.MyPaid != 110 || pb.Source != transactions.PaidBySourceMe ||
		pb.Key != "n:lee" || pb.Person.Name != "Lee" || pb.Paid {
		t.Fatalf("paid_by = %+v", pb)
	}
	if p, ok := w.PersonBalance.(DebtPerson); !ok || p.Net != -13 {
		t.Errorf("person_balance = %+v, want −13", w.PersonBalance)
	}
	if f.balance() != 890 {
		t.Errorf("wallet = %v, want 890 (1000 − 110)", f.balance())
	}

	// The debt: a received entry from the paid_by source, one record with the expense.
	entry := f.call("GET", "/api/v1/personal-debts/"+pb.DebtID.String(), nil, "getDebtEntry", http.StatusOK)
	e := obj(entry, "entry")
	src := obj(e, "source")
	if e["direction"] != "received" || num(e, "amount") != 13 || src["kind"] != "paid_by" ||
		src["id"] != row.ID.String() || src["title"] != "Dinner" || e["transaction"] != nil {
		t.Errorf("debt = %v", e)
	}
	if o := obj(e, "origin"); o["kind"] != "paid_by" || o["transaction_id"] != row.ID.String() || num(o, "amount") != 123 ||
		obj(o, "account")["id"] != f.wallet.String() {
		t.Errorf("paid_by origin = %v", o)
	}
	f.code("DELETE", "/api/v1/personal-debts/"+pb.DebtID.String(), nil, http.StatusConflict, "DEBT_MANAGED_BY_TX")
	f.code("PUT", "/api/v1/personal-debts/"+pb.DebtID.String(), map[string]any{"amount": 5}, http.StatusConflict, "ENTRY_MANAGED_BY_SOURCE")
	f.call("PUT", "/api/v1/personal-debts/"+pb.DebtID.String(), map[string]any{"paid": true}, "updateDebtEntry", http.StatusOK)

	update := func(req transactions.UpdateRequest) *transactions.WriteResult {
		t.Helper()
		out, err := f.txs.UpdateWrite(f.ctx, f.me, row.ID, req)
		if err != nil {
			t.Fatalf("update %+v: %v", req, err)
		}
		return out
	}
	// A bigger bill: they still paid 13, my wallet pays the rest.
	u := update(transactions.UpdateRequest{Amount: f64(150), Description: testdb.Str("Dinner + dessert")})
	if u.Rows[0].WalletAmount != 137 || f.balance() != 863 || u.Rows[0].PaidBy == nil || !u.Rows[0].PaidBy.Paid {
		t.Errorf("bill 150: wallet_amount %v wallet %v paid_by %+v", u.Rows[0].WalletAmount, f.balance(), u.Rows[0].PaidBy)
	}
	if v, _ := f.svc.GetEntry(f.ctx, f.me, pb.DebtID); v.Entry.Amount != 13 || testdb.Text(v.Entry.Description) != "Dinner + dessert" {
		t.Errorf("debt follows the text, keeps 13: %+v", v.Entry)
	}
	// They paid 20 instead.
	u = update(transactions.UpdateRequest{PaidBy: &transactions.PaidByInput{PersonName: "Lee", Amount: 20}})
	if u.Rows[0].WalletAmount != 130 || f.balance() != 870 {
		t.Errorf("paid_by 20: wallet_amount %v wallet %v", u.Rows[0].WalletAmount, f.balance())
	}
	if p, ok := u.PersonBalance.(DebtPerson); !ok || p.Net != -20 {
		t.Errorf("person_balance = %+v, want −20", u.PersonBalance)
	}
	// They paid it all: the wallet goes.
	u = update(transactions.UpdateRequest{PaidBy: &transactions.PaidByInput{PersonName: "lee", Amount: 150}})
	if u.Rows[0].Account != nil || u.Rows[0].WalletAmount != 0 || f.balance() != 1000 || u.Rows[0].PaidBy.DebtID != pb.DebtID {
		t.Errorf("paid all: account %v wallet_amount %v wallet %v paid_by %+v",
			u.Rows[0].Account, u.Rows[0].WalletAmount, f.balance(), u.Rows[0].PaidBy)
	}
	// A lower bill than what they paid is refused.
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, row.ID, transactions.UpdateRequest{Amount: f64(100)}); !errors.Is(err, transactions.ErrPaidByExceedsAmount) {
		t.Errorf("bill below paid_by: err = %v", err)
	}
	// Someone else paid: the old debt goes, a new one comes.
	u = update(transactions.UpdateRequest{PaidBy: &transactions.PaidByInput{PersonName: "Mom", Amount: 50}, AccountID: &f.wallet})
	if u.Rows[0].PaidBy == nil || u.Rows[0].PaidBy.Key != "n:mom" || u.Rows[0].PaidBy.DebtID == pb.DebtID || f.balance() != 900 {
		t.Errorf("Mom paid 50: %+v (wallet %v)", u.Rows[0].PaidBy, f.balance())
	}
	f.code("GET", personPath("n:lee", "?cycle=all"), nil, http.StatusNotFound, "NOT_FOUND") // Lee's debt went
	// A note-only edit keeps paid_by.
	u = update(transactions.UpdateRequest{Note: testdb.Str("all mine")})
	if u.Rows[0].PaidBy == nil || u.Rows[0].LinkMode != transactions.LinkPaid || f.balance() != 900 {
		t.Errorf("note-only update: paid_by %+v mode %s wallet %v", u.Rows[0].PaidBy, u.Rows[0].LinkMode, f.balance())
	}
	// Nobody paid for me any more.
	unset := transactions.UpdateRequest{}
	if err := json.Unmarshal([]byte(`{"paid_by": null}`), &unset); err != nil {
		t.Fatal(err)
	}
	u = update(unset)
	if u.Rows[0].PaidBy != nil || u.Rows[0].LinkMode != transactions.LinkPersonal || u.Rows[0].WalletAmount != 150 || f.balance() != 850 {
		t.Errorf("paid_by null: %+v mode %s wallet_amount %v wallet %v", u.Rows[0].PaidBy, u.Rows[0].LinkMode, u.Rows[0].WalletAmount, f.balance())
	}
	if p, ok := u.PersonBalance.(DebtPerson); !ok || p.Key != "n:mom" || p.Net != 0 {
		t.Errorf("person_balance after removal = %+v, want Mom at 0", u.PersonBalance)
	}

	// Delete: the debt goes with the expense, the wallet gets its cash part back.
	again := update(transactions.UpdateRequest{PaidBy: &transactions.PaidByInput{PersonName: "Lee", Amount: 40}})
	if f.balance() != 890 {
		t.Fatalf("re-add paid_by 40: wallet %v, want 890", f.balance())
	}
	dr, err := f.txs.DeleteWrite(f.ctx, f.me, row.ID, false, false)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if p, ok := dr.PersonBalance.(DebtPerson); !ok || p.Net != 0 || f.balance() != 1000 {
		t.Errorf("after delete: person_balance %+v wallet %v", dr.PersonBalance, f.balance())
	}
	var n int
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM personal_debts WHERE id = $1`, again.Rows[0].PaidBy.DebtID).Scan(&n); err != nil || n != 0 {
		t.Errorf("the paid-by debt outlived its expense: %d (%v)", n, err)
	}
}

// TestPaidByRules — the one-way-at-a-time and input rules.
func TestPaidByRules(t *testing.T) {
	f := newDebtHFix(t)
	base := transactions.CreateRequest{Type: transactions.TypeExpense, AccountID: &f.wallet, Amount: 100, Date: "2026-10-12"}
	cases := []struct {
		name string
		edit func(r *transactions.CreateRequest)
		want error
	}{
		{"income", func(r *transactions.CreateRequest) {
			r.Type = transactions.TypeIncome
			r.PaidBy = &transactions.PaidByInput{PersonName: "Lee", Amount: 10}
		}, transactions.ErrPaidByExpenseOnly},
		{"over the bill", func(r *transactions.CreateRequest) {
			r.PaidBy = &transactions.PaidByInput{PersonName: "Lee", Amount: 101}
		}, transactions.ErrPaidByExceedsAmount},
		{"no person", func(r *transactions.CreateRequest) {
			r.PaidBy = &transactions.PaidByInput{PersonName: "  ", Amount: 10}
		}, transactions.ErrPaidByPersonRequired},
		{"with splits", func(r *transactions.CreateRequest) {
			r.PaidBy = &transactions.PaidByInput{PersonName: "Lee", Amount: 10}
			r.Splits = []transactions.SplitInput{{PersonName: "Bee", OwedAmount: 10}}
		}, transactions.ErrLinkModeConflict},
		{"foreign contact", func(r *transactions.CreateRequest) {
			id := uuid.New()
			r.PaidBy = &transactions.PaidByInput{ContactID: &id, Amount: 10}
		}, transactions.ErrSplitContactNotFound},
	}
	for _, c := range cases {
		r := base
		c.edit(&r)
		if _, err := f.txs.CreateWrite(f.ctx, f.me, r); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if f.balance() != 1000 {
		t.Errorf("a refused create moved the wallet: %v", f.balance())
	}

	// All paid by them: no wallet even when one is sent; a contact's live name.
	r := base
	r.PaidBy = &transactions.PaidByInput{ContactID: &f.contact, Amount: 100}
	w, err := f.txs.CreateWrite(f.ctx, f.me, r)
	if err != nil {
		t.Fatal(err)
	}
	if w.Rows[0].Account != nil || f.balance() != 1000 || w.Rows[0].PaidBy.Key != "c:"+f.contact.String() ||
		w.Rows[0].PaidBy.Person.Name != "Bee Live" || w.Rows[0].CanJoinEvent {
		t.Errorf("paid in full by a contact: %+v (wallet %v)", w.Rows[0].PaidBy, f.balance())
	}
	// A paid row takes no splits.
	if _, err := f.txs.EditSplits(f.ctx, f.me, w.Rows[0].ID, []transactions.SplitEdit{{PersonName: "Bee", OwedAmount: 5}}); !errors.Is(err, transactions.ErrLinkModeConflict) {
		t.Errorf("splits on a paid row: err = %v", err)
	}
	// A split row takes no paid_by.
	s := base
	s.Splits = []transactions.SplitInput{{PersonName: "Bee", OwedAmount: 10}}
	sw, err := f.txs.CreateWrite(f.ctx, f.me, s)
	if err != nil {
		t.Fatal(err)
	}
	if sw.Rows[0].LinkMode != transactions.LinkSplit {
		t.Errorf("split row link_mode = %s", sw.Rows[0].LinkMode)
	}
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, sw.Rows[0].ID, transactions.UpdateRequest{
		PaidBy: &transactions.PaidByInput{PersonName: "Lee", Amount: 5}}); !errors.Is(err, transactions.ErrLinkModeConflict) {
		t.Errorf("paid_by on a split row: err = %v", err)
	}
}

// TestListTotalsSkipDebtCategories — the tx list totals leave out Debt Paid / Received like reports.
func TestListTotalsSkipDebtCategories(t *testing.T) {
	f := newDebtHFix(t)
	if _, err := f.txs.CreateWrite(f.ctx, f.me, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &f.wallet, Amount: 70, Date: "2026-10-12"}); err != nil {
		t.Fatal(err)
	}
	f.call("POST", repayPath("n:lee"), map[string]any{"kind": "pay", "amount": 400, "date": "2026-10-12", "account_id": f.wallet},
		"createDebtRepayment", http.StatusCreated)
	f.call("POST", repayPath("n:lee"), map[string]any{"kind": "receive", "amount": 30, "date": "2026-10-12", "account_id": f.wallet},
		"createDebtRepayment", http.StatusCreated)
	from, to := "2026-10-01", "2026-10-31"
	res, err := f.txs.List(f.ctx, f.me, transactions.ListFilter{From: &from, To: &to, Page: 1, PerPage: 50})
	if err != nil {
		t.Fatal(err)
	}
	if res.Totals.Expense != 70 || res.Totals.Income != 0 || res.Totals.Count != 3 || len(res.Data) != 3 {
		t.Errorf("totals = %+v (%d rows), want expense 70, income 0, count 3", res.Totals, len(res.Data))
	}
	for _, d := range res.Daily {
		if d.Date == "2026-10-12" && (d.Expense != 70 || d.Income != 0) {
			t.Errorf("daily 12th = %+v, want expense 70", d)
		}
	}
}

// TestPartnerSplitMakesMyExpense — a linked user's split on me lands as my expense of my share (no
// wallet, paid by them, my same-named category); "อัปเดตตาม" moves both; its money is locked.
func TestPartnerSplitMakesMyExpense(t *testing.T) {
	f := newSplitEditFixture(t)
	meFood, partnerFood := uuid.New(), uuid.New()
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO categories (id, user_id, name, type) VALUES
		($1, $2, 'Food', 'expense'), ($3, $4, ' food ', 'expense')`, meFood, f.me, partnerFood, f.partner); err != nil {
		t.Fatal(err)
	}
	created, err := f.txs.Create(f.ctx, f.me, transactions.CreateRequest{
		Type: transactions.TypeExpense, Amount: 90, Date: "2026-10-11", CategoryID: &meFood,
		Description: testdb.Str("Noodles"),
		Splits:      []transactions.SplitInput{{PersonName: "Partner", ContactID: &f.partnerContact, OwedAmount: 30}},
	})
	if err != nil {
		t.Fatal(err)
	}
	bill := created.(*transactions.TransactionDetail)
	mirror := f.mirror(t)
	if mirror == nil || mirror.Amount != 30 || mirror.Direction != DirectionReceived || mirror.PaidForTransactionID == nil {
		t.Fatalf("mirror = %+v, want received 30 with my expense", mirror)
	}
	mine, err := f.txs.Get(f.ctx, f.partner, *mirror.PaidForTransactionID)
	if err != nil {
		t.Fatal(err)
	}
	if mine.Amount != 30 || mine.Account != nil || mine.WalletAmount != 0 || mine.LinkMode != transactions.LinkPaid ||
		mine.Category == nil || mine.Category.ID != partnerFood || testdb.Text(mine.Description) != "Noodles" ||
		mine.PaidBy == nil || mine.PaidBy.Source != transactions.PaidBySourcePartner || mine.PaidBy.Amount != 30 {
		t.Fatalf("partner's expense = %+v / paid_by %+v", mine, mine.PaidBy)
	}
	// Its money is the splitter's; its category / text are the partner's own.
	if _, err := f.txs.UpdateWrite(f.ctx, f.partner, mine.ID, transactions.UpdateRequest{Amount: f64(10)}); !errors.Is(err, transactions.ErrPaidByLocked) {
		t.Errorf("edit a mirrored amount: err = %v", err)
	}
	if _, err := f.txs.UpdateWrite(f.ctx, f.partner, mine.ID, transactions.UpdateRequest{Note: testdb.Str("lunch with P")}); err != nil {
		t.Errorf("edit the note: %v", err)
	}

	// The splitter changes the split; the partner applies it: both follow.
	s := f.txs
	if _, err := s.EditSplits(f.ctx, f.me, bill.ID, []transactions.SplitEdit{{DebtID: &(*bill.Splits)[0].DebtID, OwedAmount: 45}}); err != nil {
		t.Fatal(err)
	}
	var change notifications.Notification
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM notifications WHERE recipient_user_id = $1 AND type = 'split_changed'
		ORDER BY created_at DESC LIMIT 1`, f.partner).Scan(&change.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.debts.ApplySplitChange(f.ctx, f.partner, change.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.txs.Get(f.ctx, f.partner, mine.ID); got.Amount != 45 || got.PaidBy.Amount != 45 || testdb.Text(got.Note) != "lunch with P" {
		t.Errorf("after อัปเดตตาม: amount %v paid_by %+v", got.Amount, got.PaidBy)
	}

	// Deleting my record of it keeps their debt on me.
	if _, err := f.txs.DeleteWrite(f.ctx, f.partner, mine.ID, false, false); err != nil {
		t.Fatal(err)
	}
	if m := f.mirror(t); m == nil || m.Amount != 45 || m.PaidForTransactionID != nil {
		t.Errorf("mirror after deleting the expense = %+v, want kept, unlinked", m)
	}
}

// TestPaidByLinkedPayer — a linked contact who paid for me gets the usual split_created notice with
// recipient_direction paid (they are owed); their mirror is a plain debt (no expense for them).
func TestPaidByLinkedPayer(t *testing.T) {
	f := newSplitEditFixture(t)
	before := f.mirror(t)
	w, err := f.txs.CreateWrite(f.ctx, f.me, transactions.CreateRequest{
		Type: transactions.TypeExpense, Amount: 60, Date: "2026-10-11",
		PaidBy: &transactions.PaidByInput{ContactID: &f.partnerContact, Amount: 60},
	})
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT payload FROM notifications WHERE recipient_user_id = $1 AND type = 'split_created'
		AND payload->>'split_id' = $2`, f.partner, w.Rows[0].PaidBy.DebtID.String()).Scan(&raw); err != nil {
		t.Fatalf("split_created for the payer: %v", err)
	}
	var p notifications.SplitCreatedPayload
	_ = json.Unmarshal(raw, &p)
	if p.RecipientDirection != DirectionPaid || p.Amount != 60 || p.ParentID != w.Rows[0].ID {
		t.Errorf("payload = %+v", p)
	}
	m := f.mirror(t)
	if m == nil || (before != nil && m.ID == before.ID) || m.Direction != DirectionPaid || m.PaidForTransactionID != nil {
		t.Errorf("payer's mirror = %+v, want a paid debt with no expense", m)
	}
	// Removing the paid_by tells them.
	unset := transactions.UpdateRequest{}
	_ = json.Unmarshal([]byte(`{"paid_by": null}`), &unset)
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, w.Rows[0].ID, unset); err != nil {
		t.Fatal(err)
	}
	var removed int
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'split_changed'
		AND payload->>'change' = 'removed' AND payload->>'split_id' = $2`, f.partner, w.Rows[0].PaidBy.DebtID.String()).Scan(&removed); err != nil || removed != 1 {
		t.Errorf("removed notices = %d (%v), want 1", removed, err)
	}
}
