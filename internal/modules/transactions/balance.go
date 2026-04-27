package transactions

// Balance helpers — the atomic accounts.balance maintenance flow lives here.
//
// Contract: every transaction insert / update / delete runs inside a DB tx
// that locks the affected accounts via SELECT FOR UPDATE *first*, in
// ascending UUID order, then writes both the row and the balance delta.
// Lock ordering = the only deadlock-prevention rule.

import "sort"

// signedDelta returns the signed balance delta this transaction applies.
//   - expense / transfer-out (Transfer OUT system cat): -amount
//   - income  / transfer-in  (Transfer IN  system cat): +amount
//
// `transferOut` is set on transfer rows so callers can pass the direction
// without the helper having to look up the system category.
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

// sortedLockOrder returns the `ids` sorted ascending by UUID — the lock-order
// rule. Caller passes 1 (single-account) or 2 (transfer) ids.
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
