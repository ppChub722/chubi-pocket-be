package transactions

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ErrFutureDateNotAllowed — a debt entry's transaction, an event bill's transaction or a transfer
// edit can't move to a future date.
var ErrFutureDateNotAllowed = errors.New("this transaction can't move to a future date")

type RecurringLinks interface {
	Today(ctx context.Context, userID uuid.UUID) (string, error)
	CreateUpcomingTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, req CreateRequest) (uuid.UUID, error)
	MoveToUpcomingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, req CreateRequest) (uuid.UUID, error)
	TxEditedTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *Transaction, tagIDs []uuid.UUID, tagsSet bool) error
	TxDeletingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (*uuid.UUID, error)
	ItemView(ctx context.Context, userID, itemID uuid.UUID) (any, error)
}

func (s *Service) WithRecurringLinks(l RecurringLinks) { s.recurring = l }

// isFuture — date is after the user's today (recurring not wired: never).
func (s *Service) isFuture(ctx context.Context, userID uuid.UUID, date string) (bool, error) {
	if s.recurring == nil || date == "" {
		return false, nil
	}
	today, err := s.recurring.Today(ctx, userID)
	if err != nil {
		return false, err
	}
	return date > today, nil
}

func (s *Service) createUpcoming(ctx context.Context, userID uuid.UUID, req CreateRequest) (*WriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	id, err := s.recurring.CreateUpcomingTx(ctx, tx, userID, req)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	item, err := s.recurring.ItemView(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return &WriteResult{Rows: []TransactionDetail{}, AccountBalances: []shared.WalletBalance{}, Upcoming: item}, nil
}

func (s *Service) moveToUpcoming(ctx context.Context, userID uuid.UUID, current *Transaction, req UpdateRequest) (*WriteResult, error) {
	if current.Type == TypeTransfer || current.SourcePersonalDebtID != nil || current.ProjectID != nil ||
		current.UserID != userID {
		return nil, ErrFutureDateNotAllowed
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if bill, err := linkedBillTx(ctx, tx, current); err != nil {
		return nil, err
	} else if bill != nil {
		return nil, ErrFutureDateNotAllowed
	}
	body := CreateRequest{
		Type: current.Type, AccountID: current.AccountID, Amount: current.Amount, CategoryID: current.CategoryID,
		Date: *req.Date, Description: current.Description, Note: current.Note,
	}
	if req.Amount != nil {
		body.Amount = *req.Amount
	}
	if v, ok := req.CategoryIDChange(); ok {
		body.CategoryID = v
	}
	if v, ok := req.AccountIDChange(); ok {
		body.AccountID = v
	}
	if v, ok := req.DescriptionChange(); ok {
		body.Description = v
	}
	if v, ok := req.NoteChange(); ok {
		body.Note = v
	}
	if v, ok := req.TagIDsChange(); ok {
		body.TagIDs = v
	} else {
		rows, err := tx.Query(ctx, `SELECT tt.tag_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id
			WHERE tt.transaction_id = $1 AND g.user_id = $2`, current.ID, userID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			body.TagIDs = append(body.TagIDs, id)
		}
		rows.Close()
	}
	if err := s.authorizeRowMutation(ctx, userID, current, UpdateRequest{}); err != nil {
		return nil, err
	}
	if err := s.checkNotSystemRow(ctx, current); err != nil {
		return nil, err
	}
	// The item first: the delete would break its link (ON DELETE SET NULL).
	id, err := s.recurring.MoveToUpcomingTx(ctx, tx, userID, current.ID, body)
	if err != nil {
		return nil, err
	}
	if err := s.deleteSingleInTx(ctx, tx, userID, current); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	item, err := s.recurring.ItemView(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	out, err := s.writeResult(ctx, []TransactionDetail{}, nonNilIDs(current.AccountID))
	if err != nil {
		return nil, err
	}
	out.Upcoming = item
	return out, nil
}
