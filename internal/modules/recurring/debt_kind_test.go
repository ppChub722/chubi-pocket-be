package recurring

import (
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
)

// 0.4.1-2 K: the "หนี้" kind books person-book entries.

func (f *recFix) personNet(name string) float64 {
	f.t.Helper()
	var net float64
	if err := f.pool.QueryRow(f.ctx, `SELECT COALESCE(SUM(CASE direction WHEN 'paid' THEN amount ELSE -amount END), 0)
		FROM personal_debts WHERE user_id = $1 AND counterparty_contact_id IS NULL
		AND LOWER(counterparty_person_name) = LOWER($2)`, f.me, name).Scan(&net); err != nil {
		f.t.Fatal(err)
	}
	return net
}

func (f *recFix) debtRule(body map[string]any) Detail {
	f.t.Helper()
	base := map[string]any{"name": "Mom", "kind": "debt", "person_name": "Mom", "debt_kind": "debt",
		"direction": "paid", "amount": 100, "rule": map[string]any{"freq": "daily", "interval": 1}, "start_date": f.day(-1)}
	for k, v := range body {
		base[k] = v
	}
	var d Detail
	f.call("POST", "/recurring", "createRecurring", base, 201, &d)
	f.track(d.Recurring.ID)
	return d
}

func TestDebtKindBooksEntries(t *testing.T) {
	f := newRecFix(t)
	d := f.debtRule(nil)
	r := d.Recurring
	if r.Kind != KindDebt || r.Type != typeDebt || r.Person == nil || r.Person.Name != "Mom" || av2(r.DebtKind) != DebtKindDebt ||
		av2(r.Direction) != "paid" || r.PersonBalance == nil || *r.PersonBalance != 0 || r.Category != nil {
		t.Fatalf("debt setting = %+v", r)
	}

	// K2: no category / tags / goal; a person and both choices are needed; never a money kind
	for _, bad := range []map[string]any{
		{"category_id": f.food}, {"person_name": nil}, {"debt_kind": "loan"}, {"direction": "up"}, {"amount": 0},
	} {
		body := map[string]any{"name": "Bad", "kind": "debt", "person_name": "Mom", "debt_kind": "debt", "direction": "paid",
			"amount": 10, "rule": map[string]any{"freq": "daily", "interval": 1}, "start_date": f.day(0)}
		for k, v := range bad {
			body[k] = v
		}
		f.call("POST", "/recurring", "", body, 400, nil)
	}
	f.call("PUT", "/recurring/"+r.ID.String(), "", map[string]any{"kind": "subscription"}, 400, nil)
	f.call("POST", "/recurring", "", map[string]any{"name": "Gone", "kind": "debt", "contact_id": uuid.New(), "debt_kind": "debt",
		"direction": "paid", "amount": 10, "rule": map[string]any{"freq": "daily", "interval": 1}, "start_date": f.day(0)}, 400, nil)

	// K4: confirm books the entry (ledger only: the setting has no wallet)
	var w ItemWriteResult
	f.call("POST", f.occ(r.ID, f.day(-1))+"/confirm", "confirmRecurringOccurrence", nil, 200, &w)
	if w.Item.Status != StatusDone || w.Item.DebtEntry == nil || w.Item.DebtEntry.Amount != 100 || w.Item.Transaction != nil ||
		w.Item.Person == nil || len(w.Effects.Debts) != 1 || len(w.Effects.PersonBalances) != 1 {
		t.Fatalf("confirm = %+v / effects %+v", w.Item, w.Effects)
	}
	if n := f.personNet("Mom"); n != 100 {
		t.Errorf("net after confirm = %v", n)
	}
	// Q-K2: mark-paid books the entry without a transaction
	f.call("POST", f.occ(r.ID, f.day(0))+"/mark-paid", "markRecurringOccurrencePaid", map[string]any{"amount": 40}, 200, &w)
	if w.Item.Status != StatusDone || w.Item.DebtEntry == nil || w.Item.DebtEntry.Amount != 40 {
		t.Fatalf("mark-paid = %+v", w.Item)
	}
	if n := f.personNet("Mom"); n != 140 {
		t.Errorf("net after mark-paid = %v", n)
	}
	// K6: deleting the entry skips the occurrence with its last values
	f.exec(`DELETE FROM personal_debts WHERE id = $1`, w.Item.DebtEntry.ID)
	var skipped ItemList
	f.call("GET", "/recurring-items?status=skipped&rule_id="+r.ID.String(), "listRecurringItems", nil, 200, &skipped)
	if len(skipped.Data) != 1 || skipped.Data[0].Slot != f.day(0) || skipped.Data[0].DebtEntry != nil {
		t.Errorf("after the entry was deleted = %+v", skipped.Data)
	}
	var amt float64
	if err := f.pool.QueryRow(f.ctx, `SELECT (draft->>'amount')::float8 FROM recurring_items WHERE rule_id = $1 AND slot = $2`,
		r.ID, f.day(0)).Scan(&amt); err != nil || amt != 40 {
		t.Errorf("kept amount = %v (%v)", amt, err)
	}

	// K3: an existing entry of the same person / kind / direction fills a slot
	f.exec(`INSERT INTO personal_debts (id, user_id, direction, counterparty_person_name, amount, currency, date, kind)
		VALUES ($1, $2, 'paid', 'Mom', 70, 'THB', $3, 'debt')`, uuid.New(), f.me, f.day(0))
	var entry uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM personal_debts WHERE user_id = $1 AND amount = 70`, f.me).Scan(&entry); err != nil {
		t.Fatal(err)
	}
	f.call("POST", f.occ(r.ID, f.day(0))+"/restore", "", nil, 200, nil)
	var l LinkResult
	f.call("POST", f.occ(r.ID, f.day(0))+"/link", "linkRecurringOccurrence", map[string]any{"debt_entry_id": entry}, 200, &l)
	if l.Item.DebtEntry == nil || l.Item.DebtEntry.ID != entry || l.Item.Status != StatusDone {
		t.Errorf("link entry = %+v", l.Item)
	}

	// mark-paid-ready on a debt setting books each due one without a transaction
	b := f.debtRule(map[string]any{"name": "Cee", "person_name": "Cee", "amount": 25, "start_date": f.day(-1)})
	var br BulkResult
	f.call("POST", "/recurring/"+b.Recurring.ID.String()+"/occurrences/mark-paid-ready", "markRecurringOccurrencesPaidReady", nil, 200, &br)
	if len(br.Items) != 2 || br.Items[0].DebtEntry == nil || br.Items[0].DebtEntry.TransactionID != nil {
		t.Errorf("mark-paid-ready = %+v / left out %+v", br.Items, br.LeftOut)
	}
	if n := f.personNet("Cee"); n != 50 {
		t.Errorf("Cee net after mark-paid-ready = %v", n)
	}

	// K13: the person page lists the person's debt settings
	f.debts.WithRecurring(f.svc.PersonRecurring)
	page, err := f.debts.Person(f.ctx, f.me, "n:mom", "", 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Recurring) != 1 || page.Recurring[0].ID != r.ID || page.Recurring[0].DebtKind != DebtKindDebt ||
		page.Recurring[0].Direction != "paid" || page.Recurring[0].Amount != 100 {
		t.Errorf("person page recurring = %+v", page.Recurring)
	}
}

// End to end with money (Lead 2026-10-28): I lend Bee 900 from my wallet by a "หนี้" setting
// (จดหนี้ paid, with a wallet), then a จ่ายหนี้ setting books her 300 repayments into the wallet
// until the 900 is back; the 4th round finds nothing owed.
func TestDebtKindMoneyEndToEnd(t *testing.T) {
	f := newRecFix(t)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := categories.NewService(categories.NewStore(f.pool)).SeedForUser(f.ctx, tx, f.me); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE accounts SET balance = 1000 WHERE id = $1`, f.wallet)
	wallet := func() float64 {
		var b float64
		if err := f.pool.QueryRow(f.ctx, `SELECT balance::float8 FROM accounts WHERE id = $1`, f.wallet).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}

	lend := f.debtRule(map[string]any{"name": "Lend Bee", "person_name": "Bee", "amount": 900, "account_id": f.wallet,
		"rule": map[string]any{"freq": "monthly", "interval": 1}, "start_date": f.day(0)})
	var w ItemWriteResult
	f.call("POST", f.occ(lend.Recurring.ID, f.day(0))+"/confirm", "confirmRecurringOccurrence", nil, 200, &w)
	if w.Item.DebtEntry == nil || w.Item.DebtEntry.Amount != 900 || w.Item.DebtEntry.TransactionID == nil ||
		w.Item.Transaction == nil || len(w.Effects.Debts) != 1 || len(w.Effects.PersonBalances) != 1 {
		t.Fatalf("lend 900 = %+v / effects %+v", w.Item, w.Effects)
	}
	if n, b := f.personNet("Bee"), wallet(); n != 900 || b != 100 {
		t.Fatalf("after lending: Bee owes %v (want 900), wallet %v (want 100)", n, b)
	}

	back := f.debtRule(map[string]any{"name": "Bee pays back", "person_name": "Bee", "debt_kind": "repayment",
		"direction": "received", "amount": 300, "account_id": f.wallet, "start_date": f.day(-3)})
	id := back.Recurring.ID
	for i, day := range []int{-3, -2, -1} {
		f.call("POST", f.occ(id, f.day(day))+"/confirm", "confirmRecurringOccurrence", nil, 200, &w)
		if w.Item.DebtEntry == nil || w.Item.DebtEntry.Amount != 300 || w.Item.DebtEntry.Kind != DebtKindRepayment {
			t.Fatalf("repayment %d = %+v", i+1, w.Item.DebtEntry)
		}
	}
	if n, b := f.personNet("Bee"), wallet(); n != 0 || b != 1000 {
		t.Fatalf("after 3 × 300: Bee owes %v (want 0), wallet %v (want 1000)", n, b)
	}
	var d Detail
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, &d)
	if d.Recurring.PersonBalance == nil || *d.Recurring.PersonBalance != 0 || d.Recurring.Counts.Done != 3 {
		t.Errorf("setting after paid back: balance %v counts %+v", d.Recurring.PersonBalance, d.Recurring.Counts)
	}
	f.call("POST", f.occ(id, f.day(0))+"/confirm", "", nil, 409, nil) // the 4th round: NOTHING_OWED
}

// K5: จ่ายหนี้ asks min(amount, what is owed); nothing owed for 3 rounds → no_balance; a new debt
// runs it again.
func TestDebtKindRepaymentRounds(t *testing.T) {
	f := newRecFix(t)
	f.exec(`INSERT INTO personal_debts (id, user_id, direction, counterparty_person_name, amount, currency, date, kind)
		VALUES ($1, $2, 'paid', 'Bee', 200, 'THB', $3, 'debt')`, uuid.New(), f.me, f.day(-10)) // Bee owes me 200
	d := f.debtRule(map[string]any{"name": "Bee pays back", "person_name": "Bee", "debt_kind": "repayment",
		"direction": "received", "amount": 300, "start_date": f.day(-5)})
	id := d.Recurring.ID
	if d.Recurring.NextAmount != nil && *d.Recurring.NextAmount != 300 {
		t.Logf("next amount (rule) = %v", *d.Recurring.NextAmount)
	}
	var w ItemWriteResult
	f.call("POST", f.occ(id, f.day(-5))+"/confirm", "confirmRecurringOccurrence", nil, 200, &w)
	if w.Item.DebtEntry == nil || w.Item.DebtEntry.Amount != 200 || w.Item.DebtEntry.Kind != DebtKindRepayment {
		t.Fatalf("repayment = %+v, want min(300, 200)", w.Item.DebtEntry)
	}
	if n := f.personNet("Bee"); n != 0 {
		t.Fatalf("net after repayment = %v", n)
	}
	f.call("POST", f.occ(id, f.day(-4))+"/confirm", "", nil, 409, nil) // NOTHING_OWED

	// nothing owed: each due round is skipped; the 3rd pauses it (no_balance)
	if err := f.svc.DueCheck(f.ctx, f.me, ""); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, &d)
	if d.Recurring.Status != "paused" || av2(d.Recurring.PauseReason) != PauseNoBalance || d.Recurring.ZeroRounds != 3 ||
		d.Recurring.Counts.Skipped != 3 {
		t.Fatalf("after 3 zero rounds: status %s reason %v zero %d counts %+v", d.Recurring.Status,
			d.Recurring.PauseReason, d.Recurring.ZeroRounds, d.Recurring.Counts)
	}
	// a new debt with Bee: the next due check runs it again from the current period
	f.exec(`INSERT INTO personal_debts (id, user_id, direction, counterparty_person_name, amount, currency, date, kind)
		VALUES ($1, $2, 'paid', 'bee', 50, 'THB', $3, 'debt')`, uuid.New(), f.me, f.day(0))
	// today is already checked: only the guard's "owed again" test lets the check run
	f.exec(`INSERT INTO recurring_due_checks (user_id, checked_on) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET checked_on = EXCLUDED.checked_on`, f.me, f.day(0))
	if err := f.svc.DueCheckDaily(f.ctx, f.me, ""); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/recurring/"+id.String(), "getRecurring", nil, 200, &d)
	if d.Recurring.Status != "active" || d.Recurring.ZeroRounds != 0 || d.Recurring.PersonBalance == nil || *d.Recurring.PersonBalance != 50 {
		t.Fatalf("after a new debt: status %s zero %d balance %v", d.Recurring.Status, d.Recurring.ZeroRounds, d.Recurring.PersonBalance)
	}
	var items ItemList
	f.call("GET", "/recurring-items?status=due&rule_id="+id.String(), "listRecurringItems", nil, 200, &items)
	if len(items.Data) != 1 || items.Data[0].Amount == nil || *items.Data[0].Amount != 50 {
		t.Errorf("due after resume = %+v, want one at min(300, 50)", items.Data)
	}
}
