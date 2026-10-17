package transactions

// A debt entry's wallet side (0.3.1-10 person ledger). An entry with a wallet
// owns exactly one transaction (transactions.source_personal_debt_id): paid =
// expense, received = income, in the reserved Debt Paid / Debt Received
// category. Entry and transaction are ONE record: personal_debts writes the
// transaction through the *DebtTx* methods below (no hooks fire), and an edit /
// delete made on the transaction side reaches the entry through DebtLinks.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
)

var (
	// ErrDebtTxAuthorOnly — a debt entry's wallet side belongs to its
	// author's ledger; a co-member of the wallet may not edit / delete it.
	ErrDebtTxAuthorOnly = errors.New("only its author can change a debt entry's transaction")
	// ErrDebtTxNeedsWallet — the transaction side can't drop a wallet it has;
	// to stop counting it, edit the entry (PUT /personal-debts/:id). A debt
	// transaction may also have NO wallet from the start (create_transaction
	// without account_id): that one just stays without.
	ErrDebtTxNeedsWallet = errors.New("a debt entry's transaction keeps its wallet; edit the entry to change it")
)

// DebtLinks — personal_debts' side of the link, wired in main.go.
type DebtLinks interface {
	// TxEditedTx copies the edited row's amount / date / description / note
	// onto its entry (inside the edit's DB transaction).
	TxEditedTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID, row *Transaction) error
	// TxDeletedTx deletes the entry of a deleted row; returns its person key.
	TxDeletedTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID) (key string, err error)
	// PersonBalanceByEntry / ByKey — the person's balance after commit
	// (a personal_debts.DebtPerson).
	PersonBalanceByEntry(ctx context.Context, userID, entryID uuid.UUID) (any, error)
	PersonBalanceByKey(ctx context.Context, userID uuid.UUID, key string) (any, error)
}

func (s *Service) WithDebtLinks(l DebtLinks) { s.debtLinks = l }

// DebtTxInput — the wallet side of an entry.
type DebtTxInput struct {
	Direction string // paid | received
	// AccountID nil = a transaction with no wallet (it counts in reports,
	// no wallet balance moves).
	AccountID   *uuid.UUID
	Amount      float64
	Date        string
	Description *string
	Note        *string
	// TagIDs replace the row's tags when TagsSet (create: attach).
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

// CreateDebtTxInTx writes entryID's wallet transaction and moves the
// wallet. The caller must be an active member of the wallet.
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

// SyncDebtTxInTx rewrites the wallet transaction txID to match its entry
// (direction → type + category, amount, date, texts, wallet). Returns the
// wallets touched (before and after).
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

// DeleteDebtTxInTx deletes the wallet transaction txID and gives the money
// back to its wallet. Returns that wallet (nil for an old floating row).
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

// checkDebtTxEdit — the rules for editing / deleting a row that is a debt
// entry's wallet side from the transaction side: author only, the category
// stays the reserved one, the wallet stays (see the errors above).
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
	// Dropping a wallet it has is an edit of the entry; a transaction that
	// never had one can be saved as it is.
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

// DeleteInTx deletes the caller's own row id inside tx (a transfer: both
// rows; a debt entry's wallet side: the entry too) and gives the money back.
// For module flows that remove a user's own transactions (an event deleted
// with its linked transactions, a recurring item deleted with its tx).
// Returns the wallets touched.
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
