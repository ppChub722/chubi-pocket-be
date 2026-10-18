package transactions

// Balance helpers — the atomic accounts.balance maintenance flow lives here.

import "sort"

func signedDelta(t TxType, amount float64, transferOut bool) float64 {
	switch t {
	case TypeExpense:
		return -amount
	case TypeIncome:
		return amount
	case TypeTransfer:
		if transferOut {
			return -amount
		}
		return amount
	}
	return 0
}

// sortedLockOrder returns the `ids` sorted ascending by UUID — the lock-order rule.
func sortedLockOrder(ids ...[16]byte) [][16]byte {
	out := make([][16]byte, len(ids))
	copy(out, ids)
	sort.Slice(out, func(i, j int) bool {
		for k := 0; k < 16; k++ {
			if out[i][k] != out[j][k] {
				return out[i][k] < out[j][k]
			}
		}
		return false
	})
	return out
}
