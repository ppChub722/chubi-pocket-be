package transactions

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	// ErrPrivateNoteTooLong — private_note over 500 characters (400 VALIDATION_ERROR).
	ErrPrivateNoteTooLong = apierr.Invalid("private_note: at most 500 characters", "private_note", apierr.FieldTooLong, "max", 500)
	// ErrPrivateNoteNotShared — private_note only lives on my book of a paper.
	ErrPrivateNoteNotShared = errors.New("private_note is only for my book of a split or event bill")
	// ErrNotPaperBook — the row is not my book of a paper.
	ErrNotPaperBook = errors.New("this transaction is not linked to a split or event bill")
)

// setPrivateNoteTx — my private note on my book of a paper (blank = none).
func setPrivateNoteTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *Transaction, note *string) error {
	if row.SplitTransactionID == nil {
		return ErrPrivateNoteNotShared
	}
	if note != nil && len([]rune(*note)) > 500 {
		return ErrPrivateNoteTooLong
	}
	if _, err := tx.Exec(ctx, `UPDATE transactions SET private_note = $3, updated_by_user_id = $2
		WHERE id = $1 AND user_id = $2`, row.ID, userID, shared.CleanText(note)); err != nil {
		return fmt.Errorf("private note: %w", err)
	}
	return nil
}

// UnlinkPaperBookTx — my transaction stops following its paper: a plain row from now on.
func (s *Service) UnlinkPaperBookTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (*Transaction, error) {
	cur, err := s.store.GetForUpdateTx(ctx, tx, userID, txID)
	if err != nil {
		return nil, err
	}
	if cur.SplitTransactionID == nil {
		return nil, ErrNotPaperBook
	}
	if _, err := tx.Exec(ctx, `UPDATE transactions SET split_transaction_id = NULL, split_person_id = NULL,
		updated_by_user_id = $2 WHERE id = $1`, txID, userID); err != nil {
		return nil, fmt.Errorf("unlink paper book: %w", err)
	}
	return reloadTx(ctx, tx, txID)
}

// ErrAdoptNotPlain — only my own plain expense / income can become my book of a new paper.
var ErrAdoptNotPlain = apierr.Invalid("transaction_id: only a plain expense or income of yours that is in no bill",
	"transaction_id", apierr.FieldInvalid)

// AdoptPaperBookTx — my existing plain row becomes my book of the paper (owner 2026-11-03: "แบ่ง"
// switched on in the tx edit makes a split bill around it): it takes the paper's amount / my share /
// date / description (the wallet moves by the difference); wallet, category and tags stay.
func (s *Service) AdoptPaperBookTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, b PaperBook) (*Transaction, error) {
	cur, err := s.store.GetForUpdateTx(ctx, tx, userID, txID)
	if errors.Is(err, ErrTxNotFound) {
		return nil, ErrAdoptNotPlain
	}
	if err != nil {
		return nil, err
	}
	if cur.SplitTransactionID != nil || cur.Type != b.Type || cur.TransferGroupID != nil {
		return nil, ErrAdoptNotPlain
	}
	if err := s.SyncPaperBookTx(ctx, tx, userID, txID, b.Amount, b.MyShare, b.Date, b.Description); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE transactions SET split_transaction_id = $3, split_person_id = $4,
		updated_by_user_id = $2 WHERE id = $1`, txID, userID, b.PaperID, b.PersonID); err != nil {
		return nil, fmt.Errorf("adopt paper book: %w", err)
	}
	return reloadTx(ctx, tx, txID)
}

// PaperBooksTx — my transactions following a paper (at most one per user; the map is by user).
func PaperBooksTx(ctx context.Context, q shared.Querier, paperID uuid.UUID) (map[uuid.UUID]Transaction, error) {
	rows, err := q.Query(ctx, `SELECT `+txColumns+` FROM transactions WHERE split_transaction_id = $1`, paperID)
	if err != nil {
		return nil, fmt.Errorf("paper books: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]Transaction{}
	for rows.Next() {
		t, err := scanTx(rows)
		if err != nil {
			return nil, err
		}
		out[t.UserID] = *t
	}
	return out, rows.Err()
}

// DeleteRowInTx — deletes my row (its wallet moves back); used when a paper's books go with it.
func (s *Service) DeleteRowInTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) ([]uuid.UUID, error) {
	return s.DeleteInTx(ctx, tx, userID, txID)
}

// GetDetailTx — one row as its owner sees it, read through tx.
func GetDetailTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (*TransactionDetail, error) {
	t, err := reloadTx(ctx, tx, txID)
	if err != nil {
		return nil, err
	}
	return detailOf(ctx, tx, userID, t)
}
