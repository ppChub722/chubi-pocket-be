package personal_debts

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// 0.4.1-1 D8 — the three "แบ่ง follows its tx" bugs. The fixture bill is 300 with splits Beam 100 +
// Partner 100 (a linked user, mirrored).
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

	// 3. Deleting the bill deletes its splits and tells the partner.
	before := len(f.changes(t))
	res, err := f.txs.DeleteWrite(f.ctx, f.me, f.billID, false)
	if err != nil {
		t.Fatal(err)
	}
	var left int
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM personal_debts WHERE id = ANY($1)`,
		[]any{s["Beam"].DebtID, s["Partner"].DebtID}).Scan(&left); err != nil || left != 0 {
		t.Errorf("split debts left after the bill went = %d (%v)", left, err)
	}
	ch := f.changes(t)
	if len(ch) != before+1 {
		t.Fatalf("partner split_changed %d → %d, want one more", before, len(ch))
	}
	var p struct {
		Change string `json:"change"`
	}
	_ = json.Unmarshal(ch[len(ch)-1].Payload, &p)
	if p.Change != "removed" {
		t.Errorf("partner notice change = %q, want removed", p.Change)
	}
	if ids := res.Effects.RemovedTransactionIDs; len(ids) != 1 || ids[0] != f.billID {
		t.Errorf("effects.removed_transaction_ids = %v", ids)
	}
}
