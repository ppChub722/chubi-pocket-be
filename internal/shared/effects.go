package shared

import "github.com/google/uuid"

// Effects (0.4.1-1 B2) — what a write changed, linked sides included, page-ready (openapi
// Effects). Every list is non-nil so each key answers [] when nothing of its kind changed.
// Transactions / Debts / Slots / Bills hold the module views (transactions.TransactionDetail,
// personal_debts.DebtEntry, recurring items, event board rows); shared stays free of those
// imports, so they are typed any.
type Effects struct {
	Transactions          []any           `json:"transactions"`
	RemovedTransactionIDs []uuid.UUID     `json:"removed_transaction_ids"`
	Debts                 []any           `json:"debts"`
	RemovedDebtIDs        []uuid.UUID     `json:"removed_debt_ids"`
	Slots                 []any           `json:"slots"`
	Bills                 []any           `json:"bills"`
	RemovedBillIDs        []uuid.UUID     `json:"removed_bill_ids"`
	PersonBalances        []any           `json:"person_balances"`
	AccountBalances       []WalletBalance `json:"account_balances"`
}

func NewEffects() Effects {
	return Effects{
		Transactions: []any{}, RemovedTransactionIDs: []uuid.UUID{},
		Debts: []any{}, RemovedDebtIDs: []uuid.UUID{},
		Slots: []any{}, Bills: []any{}, RemovedBillIDs: []uuid.UUID{},
		PersonBalances: []any{}, AccountBalances: []WalletBalance{},
	}
}

// Merge appends o's lists to e's (the caller avoids listing one record twice).
func (e *Effects) Merge(o Effects) {
	e.Transactions = append(e.Transactions, o.Transactions...)
	e.RemovedTransactionIDs = append(e.RemovedTransactionIDs, o.RemovedTransactionIDs...)
	e.Debts = append(e.Debts, o.Debts...)
	e.RemovedDebtIDs = append(e.RemovedDebtIDs, o.RemovedDebtIDs...)
	e.Slots = append(e.Slots, o.Slots...)
	e.Bills = append(e.Bills, o.Bills...)
	e.RemovedBillIDs = append(e.RemovedBillIDs, o.RemovedBillIDs...)
	e.PersonBalances = append(e.PersonBalances, o.PersonBalances...)
	e.AccountBalances = append(e.AccountBalances, o.AccountBalances...)
}
