package recurring

import (
	"context"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// effectsOf — what a recurring write answers as B2 effects (0.4.1-1): the occurrences it changed,
// the transactions they hold (or it let go: extra), the ones it deleted, the wallets it moved.
func (s *Service) effectsOf(ctx context.Context, userID uuid.UUID, items []Item, extra, removed []uuid.UUID,
	balances []shared.WalletBalance) shared.Effects {
	e := shared.NewEffects()
	txIDs := append([]uuid.UUID(nil), extra...)
	var (
		debtIDs []uuid.UUID
		keys    []string
	)
	for _, it := range items {
		e.Slots = append(e.Slots, it)
		switch {
		case it.DebtEntry != nil: // K4: the entry, its transaction and its person's balance
			debtIDs = append(debtIDs, it.DebtEntry.ID)
			if it.Person != nil {
				keys = append(keys, shared.DebtKey(it.Person.ContactID, it.Person.Name))
			}
		case it.Transaction != nil:
			txIDs = append(txIDs, it.Transaction.ID)
		}
	}
	if s.debts != nil && len(debtIDs) > 0 {
		if de, err := s.debts.RowEffects(ctx, s.db, userID, debtIDs, nil, keys); err == nil {
			e.Debts, e.PersonBalances = append(e.Debts, de.Debts...), append(e.PersonBalances, de.PersonBalances...)
			e.Transactions = append(e.Transactions, de.Transactions...)
		}
	}
	if s.txs != nil {
		for _, id := range uniqueIDs(txIDs) {
			if d, err := s.txs.Get(ctx, userID, id); err == nil {
				e.Transactions = append(e.Transactions, *d)
			}
		}
	}
	e.RemovedTransactionIDs = append(e.RemovedTransactionIDs, removed...)
	if balances != nil {
		e.AccountBalances = balances
	}
	return e
}

// SlotView — the occurrence of a stored row as its owner sees it (a tx delete answers it in
// effects.slots; the owner may be another member of a shared wallet).
func (s *Service) SlotView(ctx context.Context, itemID uuid.UUID) (any, error) {
	var owner uuid.UUID
	if err := s.db.QueryRow(ctx, `SELECT user_id FROM recurring_items WHERE id = $1`, itemID).Scan(&owner); err != nil {
		return nil, err
	}
	it, err := s.itemView(ctx, owner, itemID)
	if err != nil {
		return nil, err
	}
	return *it, nil
}
