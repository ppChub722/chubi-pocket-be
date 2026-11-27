package recurring

import (
	"testing"

	"github.com/google/uuid"
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
