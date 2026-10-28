package transactions

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrDebtTxAuthorOnly = errors.New("only its author can change a debt entry's transaction")
	// ErrDebtTxNeedsWallet — the transaction side can't drop a wallet it has; to stop counting it,
	// edit the entry (PUT /personal-debts/:id).
	ErrDebtTxNeedsWallet = errors.New("a debt entry's transaction keeps its wallet; edit the entry to change it")
)

type DebtLinks interface {
	TxEditedTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID, row *Transaction) error
	// TxDeletedTx — the entry's transaction was deleted (0.4.1-1 D1): the entry stays, ledger-only.
	TxDeletedTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID) (key string, err error)
	PersonBalanceByEntry(ctx context.Context, userID, entryID uuid.UUID) (any, error)
	PersonBalanceByKey(ctx context.Context, userID uuid.UUID, key string) (any, error)
	// SetPaidByTx — writes the paid-by debt of row (in nil = remove it); returns the person's key.
	SetPaidByTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *Transaction, in *PaidByInput) (key string, err error)
	// PaidByDeletingTx — row txID (with a paid-by debt) is about to be deleted: my own paid-by debt
	// goes with it (the partner is told); a partner's mirrored debt stays, unlinked. debtID = that
	// debt (nil = none), gone = it was deleted (else it was kept, unlinked).
	PaidByDeletingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (key string, debtID *uuid.UUID, gone bool, err error)
	// SplitsFollowTxTx — 0.4.1-1 D8: row (a bill) was edited: its split debts take its date;
	// checkAmount (an amount-only edit) refuses an amount below their sum (ErrSplitsExceedAmount).
	SplitsFollowTxTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *Transaction, checkAmount bool) error
	// SplitsDeletingTx — 0.4.1-1 D8: bill txID is about to be deleted: its split debts go with it
	// and linked partners are told (split_changed removed). It answers the deleted debts and their
	// people's keys (0.4.1-2 X4: the delete's effects list them).
	SplitsDeletingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (removed []uuid.UUID, keys []string, err error)
	// DebtEffects — the person-book rows (DebtEntry views) and people's balances for a write's
	// effects (0.4.1-1 B2).
	DebtEffects(ctx context.Context, userID uuid.UUID, ids []uuid.UUID, keys []string) (debts, persons []any, err error)
	// PersonNetTx — a person's balance as q sees it (0 when they have no lines), for the preview.
	PersonNetTx(ctx context.Context, q shared.Querier, userID uuid.UUID, key string) (shared.PersonRef, float64, error)
}

func (s *Service) WithDebtLinks(l DebtLinks) { s.debtLinks = l }

type DebtTxInput struct {
	Kind        string // debt | repayment: the category (LOAN_* | DEBT_*); "" = debt
	Direction   string // paid | received
	AccountID   *uuid.UUID
	Amount      float64
	Date        string
	Description *string
	Note        *string
	TagIDs      []uuid.UUID
	TagsSet     bool
}

func (in DebtTxInput) txType() TxType {
	if in.Direction == "paid" {
		return TypeExpense
	}
	return TypeIncome
}

// DebtCategoryKind — the person-book kind's system category (0.4.1-1 D5): a debt's transaction is
// LOAN_GIVEN (expense) / LOAN_TAKEN (income), a repayment's DEBT_PAID / DEBT_RECEIVED.
func DebtCategoryKind(entryKind string, t TxType) categories.SystemKind {
	switch {
	case entryKind == "repayment" && t == TypeExpense:
		return categories.SystemDebtPaid
	case entryKind == "repayment":
		return categories.SystemDebtReceived
	case t == TypeExpense:
		return categories.SystemLoanGiven
	}
	return categories.SystemLoanTaken
}

func (s *Service) debtCategory(ctx context.Context, userID uuid.UUID, entryKind string, t TxType) (uuid.UUID, error) {
	kind := DebtCategoryKind(entryKind, t)
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
	catID, err := s.debtCategory(ctx, userID, in.Kind, t)
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
	catID, err := s.debtCategory(ctx, userID, in.Kind, t)
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
	if _, err := s.deleteSingleInTx(ctx, tx, userID, current); err != nil {
		return nil, err
	}
	return current.AccountID, nil
}

// ConnectDebtTxInTx — 0.4.1-1 D2 "ผูกกับรายการที่มีอยู่": an existing transaction (checked plain by
// the caller) becomes the entry's transaction, connected + synced. It takes the kind category; its
// own is kept in debt_prev_category_id for a disconnect. Amount, date, wallet and tags stay;
// description / note are set to the values the caller passes (the entry's when set, else its own).
func (s *Service) ConnectDebtTxInTx(ctx context.Context, tx pgx.Tx, userID, entryID, txID uuid.UUID,
	entryKind string, description, note *string) (*Transaction, error) {
	current, err := scanTx(tx.QueryRow(ctx, `SELECT `+txColumns+` FROM transactions WHERE id = $1 AND user_id = $2 FOR UPDATE`, txID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTxNotFound
	}
	if err != nil {
		return nil, err
	}
	catID, err := s.debtCategory(ctx, userID, entryKind, current.Type)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE transactions
		SET source_personal_debt_id = $2, debt_connected = TRUE, debt_prev_category_id = category_id,
		    category_id = $3, description = $4, note = $5, updated_by_user_id = $6
		WHERE id = $1`, txID, entryID, catID, shared.CleanText(description), shared.CleanText(note), userID); err != nil {
		return nil, fmt.Errorf("connect debt tx: %w", err)
	}
	return reloadTx(ctx, tx, txID)
}

// DisconnectDebtTxInTx — 0.4.1-1 D2: a connected transaction goes back to a plain row with its old
// category (none when it is gone); everything else stays.
func (s *Service) DisconnectDebtTxInTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE transactions
		SET source_personal_debt_id = NULL, debt_connected = FALSE, category_id = debt_prev_category_id,
		    debt_prev_category_id = NULL, updated_by_user_id = $2
		WHERE id = $1 AND user_id = $2 AND debt_connected`, txID, userID); err != nil {
		return fmt.Errorf("disconnect debt tx: %w", err)
	}
	return nil
}

// checkLinkedEdit — the edit / delete gate for a non-transfer row: a person-book entry's
// transaction (debt or repayment) is a system-category row the author may still edit (amount, date,
// wallet, text, tags; never the category); any other system row is read-only.
func (s *Service) checkLinkedEdit(ctx context.Context, userID uuid.UUID, current *Transaction, req UpdateRequest, deleting bool) error {
	if current.SourcePersonalDebtID != nil {
		return checkDebtTxEdit(userID, current, req, deleting)
	}
	return s.checkNotSystemRow(ctx, current)
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
	if _, err := s.paidByDeletingTx(ctx, tx, userID, current); err != nil {
		return nil, err
	}
	if _, err := s.deleteSingleInTx(ctx, tx, userID, current); err != nil {
		return nil, err
	}
	if current.SourcePersonalDebtID != nil && s.debtLinks != nil {
		if _, err := s.debtLinks.TxDeletedTx(ctx, tx, userID, *current.SourcePersonalDebtID); err != nil {
			return nil, err
		}
	}
	return nonNilIDs(current.AccountID), nil
}

// debtsTouched — the person-book rows a transaction delete took along (removed) or changed, and
// their people (0.4.1-2 X4: the delete's effects list them).
type debtsTouched struct {
	removed, changed []uuid.UUID
	keys             []string
}

func (d *debtsTouched) add(o debtsTouched) {
	d.removed = append(d.removed, o.removed...)
	d.changed = append(d.changed, o.changed...)
	d.keys = append(d.keys, o.keys...)
}

// paidByDeletingTx — the paid-by debt of a row about to go (nothing when it has none).
func (s *Service) paidByDeletingTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, current *Transaction) (debtsTouched, error) {
	var out debtsTouched
	if current.PaidByAmount == nil || s.debtLinks == nil {
		return out, nil
	}
	key, id, gone, err := s.debtLinks.PaidByDeletingTx(ctx, tx, userID, current.ID)
	if err != nil || id == nil {
		return out, err
	}
	out.keys = []string{key}
	if gone {
		out.removed = []uuid.UUID{*id}
	} else {
		out.changed = []uuid.UUID{*id}
	}
	return out, nil
}
