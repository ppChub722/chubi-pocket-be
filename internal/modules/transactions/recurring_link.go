package transactions

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RecurringLinks is what the transactions module asks of recurring. A filled
// occurrence reads its transaction, so an edit needs nothing (0.4.1-1 R1); a
// delete reaches it.
type RecurringLinks interface {
	// TxDeletingTx — a transaction is being deleted (any path): the occurrence it
	// fills turns skipped with its last values (R2). It returns that occurrence's row.
	TxDeletingTx(ctx context.Context, tx pgx.Tx, txID uuid.UUID) (*uuid.UUID, error)
	// SlotView — that occurrence after the write, for effects.slots (B2).
	SlotView(ctx context.Context, itemID uuid.UUID) (any, error)
}

func (s *Service) WithRecurringLinks(l RecurringLinks) { s.recurring = l }

// recurringDeletingTx runs the recurring hook before a row goes; every delete
// path comes through deleteSingleInTx / deleteTransferInTx (R3).
func (s *Service) recurringDeletingTx(ctx context.Context, tx pgx.Tx, txID uuid.UUID) (*uuid.UUID, error) {
	if s.recurring == nil {
		return nil, nil
	}
	return s.recurring.TxDeletingTx(ctx, tx, txID)
}
