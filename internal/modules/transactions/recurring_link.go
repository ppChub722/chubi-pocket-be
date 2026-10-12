package transactions

// Recurring items (0.3.1-10). A transaction may be the linked side of a
// recurring item (the item holds the link, like an event bill). The
// transactions table only holds what really happened: a date after today
// makes an UPCOMING item instead (create), or moves the row out (edit).
// Wired from the recurring module in main.go (recurring imports
// transactions, not the other way).

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ErrFutureDateNotAllowed — a debt entry's transaction, an event bill's
// transaction or a transfer edit can't move to a future date.
var ErrFutureDateNotAllowed = errors.New("this transaction can't move to a future date")

// RecurringLinks — the recurring module's side.
type RecurringLinks interface {
	// Today — the user's calendar date (YYYY-MM-DD).
	Today(ctx context.Context, userID uuid.UUID) (string, error)
	// CreateUpcomingTx writes a one-off upcoming item from a create body.
	CreateUpcomingTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, req CreateRequest) (uuid.UUID, error)
	// MoveToUpcomingTx — row txID (deleted right after) moved to a future date:
	// its linked item takes req (and loses the link), else a one-off item.
	MoveToUpcomingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, req CreateRequest) (uuid.UUID, error)
	// TxEditedTx — also_update_link: the linked item takes the row's values.
	TxEditedTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *Transaction, tagIDs []uuid.UUID, tagsSet bool) error
	// TxDeletingTx — row txID is about to be deleted with also_delete_link:
	// its linked item goes too. Returns the item id (nil: none).
	TxDeletingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (*uuid.UUID, error)
	// ItemView — the page-ready item (spec RecurringItem), after commit.
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

// createUpcoming — POST /transactions dated after today.
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

// moveToUpcoming — PUT /transactions/:id with a date after today: the row
// leaves transactions (its money comes back) and becomes an upcoming item.
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
	// The row with the edit applied, as a create body.
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
