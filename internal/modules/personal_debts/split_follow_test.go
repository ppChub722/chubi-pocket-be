package personal_debts

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// 0.4.1-1 D8 — the three "แบ่ง follows its tx" bugs. The fixture bill is 300 with splits Beam 100 +
// Partner 100 (a linked contact; 0.4.1-2 S16: no copy in their book).
func TestSplitsFollowTheirBill(t *testing.T) {
	f := newSplitEditFixture(t)
	s := f.splits(t)

	// 1. The date follows.
	date := "2026-10-20"
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, f.billID, transactions.UpdateRequest{Date: &date}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Beam", "Partner"} {
		var got string
		if err := f.pool.QueryRow(f.ctx, `SELECT to_char(date, 'YYYY-MM-DD') FROM personal_debts WHERE id = $1`,
			s[name].DebtID).Scan(&got); err != nil || got != date {
			t.Errorf("%s split date = %q (%v), want %s", name, got, err, date)
		}
	}

	// 2. An amount-only edit can't drop below the splits (200); 200 itself is fine.
	low := 150.0
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, f.billID, transactions.UpdateRequest{Amount: &low}); !errors.Is(err, transactions.ErrSplitsExceedAmount) {
		t.Errorf("bill 300 → 150 with splits 200: err = %v, want SPLITS_EXCEED_AMOUNT", err)
	}
	ok := 200.0
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, f.billID, transactions.UpdateRequest{Amount: &ok}); err != nil {
		t.Errorf("bill → 200 (= the splits): %v", err)
	}

	// 3. Deleting the bill deletes its splits.
	res, err := f.txs.DeleteWrite(f.ctx, f.me, f.billID, false)
	if err != nil {
		t.Fatal(err)
	}
	var left int
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM personal_debts WHERE id = ANY($1)`,
		[]any{s["Beam"].DebtID, s["Partner"].DebtID}).Scan(&left); err != nil || left != 0 {
		t.Errorf("split debts left after the bill went = %d (%v)", left, err)
	}
	if ids := res.Effects.RemovedTransactionIDs; len(ids) != 1 || ids[0] != f.billID {
		t.Errorf("effects.removed_transaction_ids = %v", ids)
	}
	// 0.4.1-2 X4: the split debts are in removed_debt_ids, their people in person_balances.
	gone := map[uuid.UUID]bool{}
	for _, id := range res.Effects.RemovedDebtIDs {
		gone[id] = true
	}
	if !gone[s["Beam"].DebtID] || !gone[s["Partner"].DebtID] {
		t.Errorf("effects.removed_debt_ids = %v, want both split debts", res.Effects.RemovedDebtIDs)
	}
	if len(res.Effects.PersonBalances) < 2 {
		t.Errorf("effects.person_balances = %d people, want the 2 split people", len(res.Effects.PersonBalances))
	}
}
