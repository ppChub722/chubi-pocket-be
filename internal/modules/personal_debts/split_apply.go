package personal_debts

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// (ApplySplitChange, the "อัปเดตตาม" of a mirrored split, was retired in 0.4.1-2 S16.)

func setDebtAmountTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, amount float64) error {
	_, err := tx.Exec(ctx, `UPDATE personal_debts SET amount = $2, updated_by_user_id = user_id WHERE id = $1`, id, amount)
	return err
}

func removeDebtTx(ctx context.Context, tx pgx.Tx, d *Entry) error {
	_, err := tx.Exec(ctx, `DELETE FROM personal_debts WHERE id = $1`, d.ID)
	return err
}
