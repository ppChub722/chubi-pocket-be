package personal_debts

import (
	"errors"
	"testing"
)

// 0.4.1-1 D9 disconnect breaks the link between two books. Since 0.4.1-2 S16 a split with a linked
// contact makes no copy in the other user's book, so my split debt has no counterpart and a
// disconnect → DEBT_NOT_LINKED.
func TestDisconnectNotLinked(t *testing.T) {
	f := newSplitEditFixture(t)
	s := f.splits(t)
	mine, err := f.debts.GetEntry(f.ctx, f.me, s["Partner"].DebtID)
	if err != nil || mine.Entry.PartnerLinked {
		t.Fatalf("my split with a linked contact = %+v (%v), want no counterpart", mine, err)
	}
	if _, err := f.debts.Disconnect(f.ctx, f.me, s["Partner"].DebtID); !errors.Is(err, ErrDebtNotLinked) {
		t.Errorf("disconnect: err = %v, want DEBT_NOT_LINKED", err)
	}
}
