package accounts

import "testing"

// The "shared pot": the summed balance of the shared wallets in the user's
// currency (signed), next to how many there are.
func TestTotalsSharedBalance(t *testing.T) {
	accs := []Account{
		{Type: TypeBank, Group: "asset", Currency: "THB", Balance: 1000, IsShared: true, TrackBalance: true},
		{Type: TypeCash, Group: "asset", Currency: "THB", Balance: 200, TrackBalance: true},
		{Type: TypeCreditCard, Group: "liability", Currency: "THB", Balance: -300, IsShared: true, TrackBalance: true},
		{Type: TypeBank, Group: "asset", Currency: "USD", Balance: 50, IsShared: true, TrackBalance: true},
	}
	got := buildTotals(accs, "THB")
	if got.SharedCount != 3 || got.SharedBalance != 700 || got.OtherCurrencyCount != 1 {
		t.Errorf("totals = %+v, want shared_count 3 (incl. the USD one), shared_balance 700 (THB only), 1 other currency", got)
	}
}
