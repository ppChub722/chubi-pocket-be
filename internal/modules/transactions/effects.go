package transactions

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// DetailsTx — the rows as the caller sees them (detail shape, splits filled), read through q so a
// write can list them in its effects before it commits (0.4.1-1 B2). Unknown ids are skipped.
func (s *Service) DetailsTx(ctx context.Context, q shared.Querier, userID uuid.UUID, ids []uuid.UUID) ([]TransactionDetail, error) {
	if len(ids) == 0 {
		return []TransactionDetail{}, nil
	}
	rows, err := q.Query(ctx, `SELECT `+txColumns+` FROM transactions WHERE id = ANY($1) ORDER BY date, created_at, id`, ids)
	if err != nil {
		return nil, fmt.Errorf("effect rows: %w", err)
	}
	var list []Transaction
	for rows.Next() {
		t, err := scanTx(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, *t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buildDetails(ctx, q, userID, list, true)
}

// rowEffects — a transaction write's effects (0.4.1-1 B2): the rows, the person-book rows they
// carry (their splits, paid-by debt, the entry they are the transaction of) plus extra debts, the
// people touched and the wallets. BE-2's recurring / event hooks add slots and bills.
func (s *Service) rowEffects(ctx context.Context, userID uuid.UUID, rows []TransactionDetail, removedTxs []uuid.UUID,
	debtIDs []uuid.UUID, keys []string, balances []shared.WalletBalance) (shared.Effects, error) {
	out := shared.NewEffects()
	for _, r := range rows {
		out.Transactions = append(out.Transactions, r)
		if r.Counterparty != nil {
			debtIDs = append(debtIDs, r.Counterparty.EntryID)
		}
		if r.PaidBy != nil {
			debtIDs, keys = append(debtIDs, r.PaidBy.DebtID), append(keys, r.PaidBy.Key)
		}
		if r.Splits != nil {
			for _, sp := range *r.Splits {
				debtIDs, keys = append(debtIDs, sp.DebtID), append(keys, sp.Key)
			}
		}
	}
	out.RemovedTransactionIDs = append(out.RemovedTransactionIDs, removedTxs...)
	if balances != nil {
		out.AccountBalances = balances
	}
	if s.debtLinks != nil && (len(debtIDs) > 0 || len(keys) > 0) {
		debts, persons, err := s.debtLinks.DebtEffects(ctx, userID, debtIDs, keys)
		if err != nil {
			return out, err
		}
		out.Debts, out.PersonBalances = debts, persons
	}
	return out, nil
}
