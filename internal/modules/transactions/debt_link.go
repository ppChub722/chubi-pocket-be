package transactions

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
)

var (
	ErrDebtTxAuthorOnly = errors.New("only its author can change a debt entry's transaction")
	// ErrDebtTxNeedsWallet — the transaction side can't drop a wallet it has; to stop counting it,
	// edit the entry (PUT /personal-debts/:id).
	ErrDebtTxNeedsWallet = errors.New("a debt entry's transaction keeps its wallet; edit the entry to change it")
)

type DebtLinks interface {
	TxEditedTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID, row *Transaction) error
	TxDeletedTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID) (key string, err error)
	PersonBalanceByEntry(ctx context.Context, userID, entryID uuid.UUID) (any, error)
	PersonBalanceByKey(ctx context.Context, userID uuid.UUID, key string) (any, error)
}

func (s *Service) WithDebtLinks(l DebtLinks) { s.debtLinks = l }

type DebtTxInput struct {
	Direction string // paid | received
	AccountID   *uuid.UUID
	Amount      float64
	Date        string
	Description *string
	Note        *string
	TagIDs  []uuid.UUID
	TagsSet bool
}

func (in DebtTxInput) txType() TxType {
	if in.Direction == "paid" {
		return TypeExpense
	}
	return TypeIncome
}

func (s *Service) debtCategory(ctx context.Context, userID uuid.UUID, t TxType) (uuid.UUID, error) {
	kind := categories.SystemDebtReceived
	if t == TypeExpense {
		kind = categories.SystemDebtPaid
	}
	c, err := s.cats.SystemFor(ctx, userID, kind)
	if err != nil {
		return uuid.Nil, fmt.Errorf("debt category %s: %w", kind, err)
	}
	return c.ID, nil
}

func (s *Service) CreateDebtTxInTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID, in DebtTxInput) (*Transaction, error) {
	if in.AccountID != nil {
		if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*in.AccountID}); err != nil {
			return nil, err
		}
		if _, _, err := s.store.GetAccountBalanceTx(ctx, tx, userID, *in.AccountID); err != nil {
			return nil, err
		}
	}
	t := in.txType()
	catID, err := s.debtCategory(ctx, userID, t)
	if err != nil {
		return nil, err
	}
	created, err := s.store.InsertRowTx(ctx, tx, &Transaction{
		UserID: userID, AccountID: in.AccountID, Type: t, Amount: in.Amount, CategoryID: &catID,
		Date: in.Date, Description: in.Description, Note: in.Note, SourcePersonalDebtID: &entryID,
	})
	if err != nil {
		return nil, err
	}
	if in.AccountID != nil {
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *in.AccountID, signedDelta(t, in.Amount, false)); err != nil {
			return nil, err
		}
	}
	if err := s.store.AttachTagsTx(ctx, tx, userID, created.ID, in.TagIDs); err != nil {
		return nil, err
	}
	return created, nil
}

func (s *Service) SyncDebtTxInTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, in DebtTxInput) ([]uuid.UUID, error) {
	current, err := scanTx(tx.QueryRow(ctx, `SELECT `+txColumns+` FROM transactions WHERE id = $1 AND user_id = $2 FOR UPDATE`, txID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTxNotFound
	}
	if err != nil {
		return nil, err
	}
	touched := nonNilIDs(current.AccountID, in.AccountID)
	lockIDs := uniqueUUIDs(touched)
	if err := s.store.LockAccountsForUpdate(ctx, tx, sortedUUIDs(lockIDs)); err != nil {
		return nil, err
	}
	if in.AccountID != nil {
		if _, _, err := s.store.GetAccountBalanceTx(ctx, tx, userID, *in.AccountID); err != nil {
			return nil, err
		}
	}
	if current.AccountID != nil {
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *current.AccountID,
			-signedDelta(current.Type, current.Amount, false)); err != nil {
			return nil, err
		}
	}
	t := in.txType()
	if in.AccountID != nil {
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *in.AccountID, signedDelta(t, in.Amount, false)); err != nil {
			return nil, err
		}
	}
	catID, err := s.debtCategory(ctx, userID, t)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE transactions
		SET type = $2, category_id = $3, amount = $4, date = $5::date, description = $6, note = $7,
		    account_id = $8, updated_by_user_id = $9
		WHERE id = $1`, txID, t, catID, in.Amount, in.Date, in.Description, in.Note, in.AccountID, userID); err != nil {
		return nil, fmt.Errorf("sync debt tx: %w", err)
	}
	if in.TagsSet {
		if err := s.store.ReplaceTagsTx(ctx, tx, userID, txID, in.TagIDs); err != nil {
			return nil, err
		}
	}
	return touched, nil
}

func (s *Service) DeleteDebtTxInTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (*uuid.UUID, error) {
	current, err := scanTx(tx.QueryRow(ctx, `SELECT `+txColumns+` FROM transactions WHERE id = $1 AND user_id = $2 FOR UPDATE`, txID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTxNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.deleteSingleInTx(ctx, tx, userID, current); err != nil {
		return nil, err
	}
	return current.AccountID, nil
}

func checkDebtTxEdit(userID uuid.UUID, current *Transaction, req UpdateRequest, deleting bool) error {
	if current.UserID != userID {
		return ErrDebtTxAuthorOnly
	}
	if deleting {
		return nil
	}
	if _, ok := req.CategoryIDChange(); ok {
		return ErrSystemTransactionImmutable
	}
	// Dropping a wallet it has is an edit of the entry; a transaction that never had one can be saved
	// as it is.
	if acc, ok := req.AccountIDChange(); ok && acc == nil && current.AccountID != nil {
		return ErrDebtTxNeedsWallet
	}
	return nil
}

func sortedUUIDs(ids []uuid.UUID) []uuid.UUID {
	out := append([]uuid.UUID(nil), ids...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && uuidLess(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (s *Service) DeleteInTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) ([]uuid.UUID, error) {
	current, err := scanTx(tx.QueryRow(ctx, `SELECT `+txColumns+` FROM transactions WHERE id = $1 AND user_id = $2 FOR UPDATE`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTxNotFound
	}
	if err != nil {
		return nil, err
	}
	if current.Type == TypeTransfer {
		return s.deleteTransferInTx(ctx, tx, userID, current)
	}
	if current.SourcePersonalDebtID == nil {
		if err := s.checkNotSystemRow(ctx, current); err != nil {
			return nil, err
		}
	}
	if err := s.deleteSingleInTx(ctx, tx, userID, current); err != nil {
		return nil, err
	}
	if current.SourcePersonalDebtID != nil && s.debtLinks != nil {
		if _, err := s.debtLinks.TxDeletedTx(ctx, tx, userID, *current.SourcePersonalDebtID); err != nil {
			return nil, err
		}
	}
	return nonNilIDs(current.AccountID), nil
}
