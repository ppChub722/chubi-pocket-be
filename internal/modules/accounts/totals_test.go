package accounts

import "testing"

func TestTotalsSharedBalance(t *testing.T) {
	accs := []Account{
		{Type: TypeBank, Group: "asset", Currency: "THB", Balance: 1000, IsShared: true, TrackBalance: true},
		{Type: TypeCash, Group: "asset", Currency: "THB", Balance: 200, TrackBalance: true},
		{Type: TypeCreditCard, Group: "liability", Currency: "THB", Balance: -300, IsShared: true, TrackBalance: true},
		{Type: TypeBank, Group: "asset", Currency: "USD", Balance: 50, IsShared: true, TrackBalance: true},
	}
	got := buildTotals(accs, "THB")
	if got.SharedCount != 3 || got.SharedBalance != 750 {
		t.Errorf("totals = %+v, want shared_count 3, shared_balance 750 (currency ignored)", got)
	}
}
