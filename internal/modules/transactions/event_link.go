package transactions

// A transaction an event bill was made from (owner 2026-10-13: linked by
// PROMPT, not sync). An amount / date edit here changes the bill only when
// the caller asks for it (UpdateRequest.AlsoUpdateLink); the bill's side
// works the same way (projects). Wired from projects in main.go (projects
// imports transactions, not the other way).

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EventBillSyncer gives bill ptID the edited transaction's amount / date,
// inside the edit's DB transaction.
type EventBillSyncer func(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID, amount float64, date string) error

func (s *Service) WithEventBillSyncer(fn EventBillSyncer) { s.eventBills = fn }

// EventBillDeleter removes the bill made from transaction txID when userID
// may edit its event (DELETE /transactions ?also_delete_link), inside the
// delete's DB transaction, before the row goes. Returns the bill id (nil:
// none, or not allowed — it stays, unlinked).
type EventBillDeleter func(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (*uuid.UUID, error)

func (s *Service) WithEventBillDeleter(fn EventBillDeleter) { s.billDeleter = fn }

// linkedBillTx — the board bill made from t (project_transactions.
// linked_transaction_id), else nil.
func linkedBillTx(ctx context.Context, tx pgx.Tx, t *Transaction) (*uuid.UUID, error) {
	if t.Type == TypeTransfer {
		return nil, nil
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM project_transactions
		WHERE linked_transaction_id = $1 AND parent_project_transaction_id IS NULL`, t.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("linked bill: %w", err)
	}
	return &id, nil
}
