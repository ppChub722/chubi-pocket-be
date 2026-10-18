package transactions

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type EventBillSyncer func(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID, amount float64, date string) error

func (s *Service) WithEventBillSyncer(fn EventBillSyncer) { s.eventBills = fn }

type EventBillDeleter func(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (*uuid.UUID, error)

func (s *Service) WithEventBillDeleter(fn EventBillDeleter) { s.billDeleter = fn }

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
