package personal_debts

import (
	"errors"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

func modep(m string) *string { return &m }

// TestLinkModeSwitchAndPreview — 0.4.0-3 B3: a split bill switches to "Lee paid part of it"; the
// preview shows the people / wallet moves without saving; the switch drops the splits and writes the
// paid-by debt; then back to personal.
func TestLinkModeSwitchAndPreview(t *testing.T) {
	f := newDebtHFix(t)
	w, err := f.txs.CreateWrite(f.ctx, f.me, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &f.wallet, Amount: 300, Date: "2026-10-12",
		Splits: []transactions.SplitInput{{PersonName: "Bee", OwedAmount: 100}},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := w.Rows[0].ID
	if f.balance() != 700 || w.Rows[0].LinkMode != transactions.LinkSplit {
		t.Fatalf("split bill: wallet %v mode %s", f.balance(), w.Rows[0].LinkMode)
	}
	toPaid := transactions.UpdateRequest{LinkMode: modep("paid"), PaidBy: &transactions.PaidByInput{PersonName: "Lee", Amount: 50}}

	p, err := f.txs.Preview(f.ctx, f.me, id, toPaid)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	people := map[string]transactions.PersonDelta{}
	for _, d := range p.People {
		people[d.Key] = d
	}
	if !p.Preview || len(p.Rows) != 1 || p.Rows[0].LinkMode != transactions.LinkPaid || p.Rows[0].WalletAmount != 250 {
		t.Fatalf("preview row = %+v", p.Rows)
	}
	if b := people["n:bee"]; b.Before != 100 || b.After != 0 || b.Delta != -100 {
		t.Errorf("Bee = %+v, want 100 → 0", b)
	}
	if l := people["n:lee"]; l.Before != 0 || l.After != -50 || l.Delta != -50 || l.Person.Name != "Lee" {
		t.Errorf("Lee = %+v, want 0 → −50", l)
	}
	if len(p.Wallets) != 1 || p.Wallets[0].Before != 700 || p.Wallets[0].After != 750 || p.Wallets[0].Delta != 50 {
		t.Errorf("wallets = %+v, want 700 → 750", p.Wallets)
	}
	// Nothing was saved.
	if d, _ := f.txs.Get(f.ctx, f.me, id); d.LinkMode != transactions.LinkSplit || f.balance() != 700 {
		t.Fatalf("preview saved something: mode %s wallet %v", d.LinkMode, f.balance())
	}

	u, err := f.txs.UpdateWrite(f.ctx, f.me, id, toPaid)
	if err != nil {
		t.Fatalf("switch to paid: %v", err)
	}
	if u.Rows[0].LinkMode != transactions.LinkPaid || u.Rows[0].SplitCount != 0 || f.balance() != 750 || len(u.PersonBalances) != 2 {
		t.Errorf("after the switch: mode %s splits %d wallet %v balances %v",
			u.Rows[0].LinkMode, u.Rows[0].SplitCount, f.balance(), u.PersonBalances)
	}

	// Rules.
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, id, transactions.UpdateRequest{LinkMode: modep("split")}); !errors.Is(err, transactions.ErrLinkModeInput) {
		t.Errorf("split without splits: err = %v", err)
	}
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, id, transactions.UpdateRequest{LinkMode: modep("personal"),
		PaidBy: &transactions.PaidByInput{PersonName: "Lee", Amount: 5}}); !errors.Is(err, transactions.ErrLinkModeConflict) {
		t.Errorf("personal with paid_by: err = %v", err)
	}
	splits := []transactions.SplitEdit{{PersonName: "Bee", OwedAmount: 30}}
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, id, transactions.UpdateRequest{Splits: &splits}); !errors.Is(err, transactions.ErrLinkModeConflict) {
		t.Errorf("splits on a paid row without link_mode: err = %v", err)
	}

	// paid → split in one call: the paid-by debt goes, the split comes.
	u, err = f.txs.UpdateWrite(f.ctx, f.me, id, transactions.UpdateRequest{LinkMode: modep("split"), Splits: &splits})
	if err != nil {
		t.Fatalf("switch to split: %v", err)
	}
	if u.Rows[0].LinkMode != transactions.LinkSplit || u.Rows[0].PaidBy != nil || f.balance() != 700 {
		t.Errorf("paid → split: mode %s paid_by %+v wallet %v", u.Rows[0].LinkMode, u.Rows[0].PaidBy, f.balance())
	}
	// split → personal: the splits go.
	u, err = f.txs.UpdateWrite(f.ctx, f.me, id, transactions.UpdateRequest{LinkMode: modep("personal")})
	if err != nil {
		t.Fatalf("switch to personal: %v", err)
	}
	if u.Rows[0].LinkMode != transactions.LinkPersonal || u.Rows[0].MyShare == nil || *u.Rows[0].MyShare != 300 {
		t.Errorf("personal: %+v share %v", u.Rows[0].LinkMode, u.Rows[0].MyShare)
	}
	var n int
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1`, f.me).Scan(&n); err != nil || n != 0 {
		t.Errorf("debts left = %d (%v), want 0", n, err)
	}
}
