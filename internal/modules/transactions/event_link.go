package transactions

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type EventBillSyncer func(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID, amount float64, date string) error

func (s *Service) WithEventBillSyncer(fn EventBillSyncer) { s.eventBills = fn }

type EventBillDeleter func(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (*uuid.UUID, error)

func (s *Service) WithEventBillDeleter(fn EventBillDeleter) { s.billDeleter = fn }

// EventLinks (0.4.0-3 B3) — what the transactions module asks of events: a row switching into /
// out of an event, and my row → its bill (apply_to_event). Implemented by projects.Service.
type EventLinks interface {
	JoinTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *Transaction, projectID uuid.UUID, people shared.BillPeople) error
	LeaveTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) error
	PushTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *Transaction) error
}

func (s *Service) WithEventLinks(l EventLinks) { s.eventLinks = l }

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
