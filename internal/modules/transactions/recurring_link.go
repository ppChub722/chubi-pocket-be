package transactions

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RecurringLinks is what the transactions module asks of recurring: a tx edit
// or delete reaches the recurring item the tx is linked to.
type RecurringLinks interface {
	TxEditedTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *Transaction, tagIDs []uuid.UUID, tagsSet bool) error
	// TxDeletingTx — a transaction is being deleted: the occurrence it records goes back to
	// its state before (computed again; paid if it was cleared first), or is skipped.
	TxDeletingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, skip bool) (*uuid.UUID, error)
}

func (s *Service) WithRecurringLinks(l RecurringLinks) { s.recurring = l }
