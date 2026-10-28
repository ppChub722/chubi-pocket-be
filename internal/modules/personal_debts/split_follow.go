package personal_debts

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// 0.4.1-1 D8 — "แบ่ง" follows its transaction: the split debts of a bill (source transaction) take
// its date, an amount-only edit can't drop the bill below their sum, and deleting the bill deletes
// them (a linked partner gets split_changed "removed", as when a split is removed by hand).

func (s *Service) SplitsFollowTxTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *transactions.Transaction, checkAmount bool) error {
	if checkAmount {
		var sum float64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0)::float8 FROM personal_debts
			WHERE source_transaction_id = $1 AND user_id = $2 AND source_kind = 'transaction'`, row.ID, userID).Scan(&sum); err != nil {
			return fmt.Errorf("split sum: %w", err)
		}
		if sum > row.Amount+amountEps {
			return transactions.ErrSplitsExceedAmount
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debts SET date = $3::date, updated_by_user_id = $2
		WHERE source_transaction_id = $1 AND user_id = $2 AND source_kind = 'transaction' AND date <> $3::date`,
		row.ID, userID, row.Date); err != nil {
		return fmt.Errorf("split date: %w", err)
	}
	return nil
}

func (s *Service) SplitsDeletingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) ([]uuid.UUID, []string, error) {
	rows, err := tx.Query(ctx, `SELECT `+entryColumns+` FROM personal_debts
		WHERE source_transaction_id = $1 AND user_id = $2 AND source_kind = 'transaction' FOR UPDATE`, txID, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("bill splits: %w", err)
	}
	var splits []*Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		splits = append(splits, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	ids := make([]uuid.UUID, 0, len(splits))
	keys := make([]string, 0, len(splits))
	for _, e := range splits {
		ids, keys = append(ids, e.ID), append(keys, e.Key())
	}
	if len(ids) == 0 {
		return nil, nil, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM personal_debts WHERE id = ANY($1)`, ids); err != nil {
		return nil, nil, fmt.Errorf("delete bill splits: %w", err)
	}
	return ids, keys, nil
}
