package personal_debts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// The person book (0.4.1-1 D3 / D2 / D10): every row — a debt or a repayment — counts in the
// person's balance and has an optional transaction, connected and synced (amount, date,
// description, note are one value). The transaction is made from the row (create_transaction) or
// an existing one is connected (link_transaction_id; it can be disconnected later).

var (
	// ErrInvalidInput — a request the contract refuses (400 VALIDATION_ERROR).
	ErrInvalidInput = errors.New("invalid input")
	// ErrEntryHasTx — connecting a transaction to an entry that already has one.
	ErrEntryHasTx = errors.New("this entry already has a transaction: delete or disconnect it first")
	// ErrTxNotDisconnectable — only a transaction connected later can be disconnected.
	ErrTxNotDisconnectable = errors.New("only a transaction connected later can be disconnected; delete the transaction instead")
)

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidInput, msg) }

// EntryInput — one person-book row to write, the person and the date resolved. The HTTP creates and
// E8's "book bill" call (projects) both write through CreateEntryTx.
type EntryInput struct {
	Kind        string // debt | repayment ("" = debt)
	Direction   string // paid (net +) | received (net −)
	ContactID   *uuid.UUID
	PersonName  string
	Amount      float64 // > 0; ignored with LinkTransactionID (the transaction's amount)
	Date        string  // YYYY-MM-DD; ignored with LinkTransactionID (the transaction's date)
	Description *string
	Note        *string
	DueDate     *string // debts only
	// The transaction side: CreateTransaction (wallet / tags optional), or LinkTransactionID, or none.
	CreateTransaction bool
	AccountID         *uuid.UUID
	TagIDs            []uuid.UUID
	LinkTransactionID *uuid.UUID
	// ProjectID — the event a payback paper's repayment row belongs to (lump bookkeeping).
	ProjectID *uuid.UUID
}

// CreateEntryTx writes the row (and its transaction) inside the caller's DB transaction and answers
// its effects, read through tx.
func (s *Service) CreateEntryTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, in EntryInput) (*Entry, shared.Effects, error) {
	e, wallets, err := s.insertRowTx(ctx, tx, userID, in)
	if err != nil {
		return nil, shared.Effects{}, err
	}
	eff, err := s.effects(ctx, tx, userID, touch{debts: []uuid.UUID{e.ID}, keys: []string{e.Key()}, wallets: wallets})
	if err != nil {
		return nil, shared.Effects{}, err
	}
	return e, eff, nil
}

// insertRowTx — the row, then its transaction; answers the wallets it moved.
func (s *Service) insertRowTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, in EntryInput) (*Entry, []uuid.UUID, error) {
	if in.Kind == "" {
		in.Kind = KindDebt
	}
	if in.LinkTransactionID != nil && (in.CreateTransaction || in.AccountID != nil || len(in.TagIDs) > 0) {
		return nil, nil, invalid("link_transaction_id goes alone: no create_transaction, account_id or tag_ids")
	}
	if !in.CreateTransaction && (in.AccountID != nil || len(in.TagIDs) > 0) {
		return nil, nil, ErrLedgerOnlyNoWallet
	}
	if in.Kind == KindRepayment && in.DueDate != nil {
		return nil, nil, invalid("a repayment has no due_date")
	}
	if in.LinkTransactionID != nil {
		row, err := linkableTx(ctx, tx, userID, *in.LinkTransactionID, in.Direction)
		if err != nil {
			return nil, nil, err
		}
		in.Amount, in.Date = row.Amount, row.Date
		in.Description, in.Note = firstText(in.Description, row.Description), firstText(in.Note, row.Note)
	}
	if in.Amount <= 0 {
		return nil, nil, invalid("amount is required (> 0)")
	}
	if in.DueDate != nil && *in.DueDate < in.Date {
		return nil, nil, ErrDueBeforeDate
	}
	entry := &Entry{
		UserID: userID, Kind: in.Kind, Direction: in.Direction, ContactID: in.ContactID, PersonName: in.PersonName,
		Amount: in.Amount, Date: in.Date, Description: in.Description, Note: in.Note, DueDate: in.DueDate,
		ProjectID: in.ProjectID,
	}
	e, err := insertEntryTx(ctx, tx, entry)
	if err != nil {
		return nil, nil, err
	}
	var wallets []uuid.UUID
	switch {
	case in.CreateTransaction:
		if _, err := s.txs.CreateDebtTxInTx(ctx, tx, userID, e.ID, transactions.DebtTxInput{
			Kind: e.Kind, Direction: e.Direction, AccountID: in.AccountID, Amount: e.Amount, Date: e.Date,
			Description: e.Description, Note: e.Note, TagIDs: in.TagIDs,
		}); err != nil {
			return nil, nil, err
		}
		wallets = nonNil(in.AccountID)
	case in.LinkTransactionID != nil:
		if _, err := s.txs.ConnectDebtTxInTx(ctx, tx, userID, e.ID, *in.LinkTransactionID, e.Kind, e.Description, e.Note); err != nil {
			return nil, nil, err
		}
	}
	return e, wallets, nil
}

func firstText(a, b *string) *string {
	if c := shared.CleanText(a); c != nil {
		return c
	}
	return shared.CleanText(b)
}

type linkable struct {
	Amount      float64
	Date        string
	Description *string
	Note        *string
}

// linkableTx — txID may be connected to a row of direction: one of my plain transactions of its type
// (paid = expense, received = income): not a transfer, not another row's, not in an event, not a split
// bill, no paid_by, not a system-category row, not filling a recurring slot. Locks the row.
func linkableTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, direction string) (*linkable, error) {
	var (
		l     linkable
		typ   string
		taken bool
		plain bool
	)
	err := tx.QueryRow(ctx, `SELECT t.amount::float8, to_char(t.date, 'YYYY-MM-DD'), t.description, t.note, t.type,
		t.source_personal_debt_id IS NOT NULL,
		t.transfer_group_id IS NULL AND t.split_transaction_id IS NULL
		AND NOT EXISTS (SELECT 1 FROM categories c WHERE c.id = t.category_id AND c.is_system)
		AND NOT EXISTS (SELECT 1 FROM recurring_items ri WHERE ri.transaction_id = t.id)
		FROM transactions t WHERE t.id = $1 AND t.user_id = $2 FOR UPDATE OF t`, txID, userID).
		Scan(&l.Amount, &l.Date, &l.Description, &l.Note, &typ, &taken, &plain)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, transactions.ErrTxNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("linkable tx: %w", err)
	}
	if taken {
		return nil, ErrTxLinkedElsewhere
	}
	want := "income"
	if direction == DirectionPaid {
		want = "expense"
	}
	if !plain || typ != want {
		return nil, ErrTxNotLinkable
	}
	return &l, nil
}

// touch — what a write changed, for its effects.
type touch struct {
	debts, removedDebts []uuid.UUID
	txs, removedTxs     []uuid.UUID // the debts' own transactions are added by effects
	keys                []string
	wallets             []uuid.UUID
}

// effects — the B2 answer of a write, read through q (tx inside a write, the pool after commit).
func (s *Service) effects(ctx context.Context, q shared.Querier, userID uuid.UUID, t touch) (shared.Effects, error) {
	out := shared.NewEffects()
	views, err := loadViews(ctx, q, `WHERE pd.user_id = $1 AND pd.id = ANY($2) ORDER BY pd.date, pd.created_at, pd.id`,
		userID, uniqueIDs(t.debts))
	if err != nil {
		return out, err
	}
	txIDs := append([]uuid.UUID{}, t.txs...)
	keys := append([]string{}, t.keys...)
	for _, v := range views {
		out.Debts = append(out.Debts, v)
		if v.Transaction != nil {
			txIDs = append(txIDs, v.Transaction.ID)
		}
		keys = append(keys, v.Key)
	}
	rows, err := s.txs.DetailsTx(ctx, q, userID, uniqueIDs(txIDs))
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Transactions = append(out.Transactions, r)
	}
	out.RemovedDebtIDs = append(out.RemovedDebtIDs, uniqueIDs(t.removedDebts)...)
	out.RemovedTransactionIDs = append(out.RemovedTransactionIDs, uniqueIDs(t.removedTxs)...)
	seen := map[string]bool{}
	for _, k := range keys {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		b, err := s.balanceOfKey(ctx, q, userID, k)
		if err != nil {
			return out, err
		}
		out.PersonBalances = append(out.PersonBalances, b)
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, q, uniqueIDs(t.wallets)); err != nil {
		return out, err
	}
	if out.AccountBalances == nil {
		out.AccountBalances = []shared.WalletBalance{}
	}
	return out, nil
}

// itemOf — the row as its person-page statement line (running balance, cycle).
func (s *Service) itemOf(ctx context.Context, userID uuid.UUID, key string, id uuid.UUID) (*StatementItem, error) {
	lines, err := s.store.ledgerLines(ctx, s.store.db, userID, key)
	if err != nil {
		return nil, err
	}
	mark := cycleMarkOf(lines)
	for _, l := range lines {
		if l.ID != id {
			continue
		}
		byID, err := s.store.statementItems(ctx, s.store.db, userID, []ledgerLine{l}, mark)
		if err != nil {
			return nil, err
		}
		if it, ok := byID[id]; ok {
			return &it, nil
		}
	}
	return nil, nil
}

// markAllPaidTx — "ปิดหนี้" (D10): every open debt of the person a repayment of direction pays down
// (paid = I pay back → their received debts; received → their paid debts) gets the paid label.
func markAllPaidTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, key, repayDirection string) error {
	if _, err := tx.Exec(ctx, `UPDATE personal_debts SET paid = TRUE, updated_by_user_id = $1
		WHERE user_id = $1 AND kind = 'debt' AND NOT paid AND direction = $3
		  AND `+shared.DebtKeySQL("personal_debts")+` = $2`, userID, key, opposite(repayDirection)); err != nil {
		return fmt.Errorf("mark all paid: %w", err)
	}
	return nil
}

var _ interface {
	DebtEffects(ctx context.Context, userID uuid.UUID, ids []uuid.UUID, keys []string) ([]any, []any, error)
} = (*Service)(nil)

// DebtEffects — for a transaction write's effects: the rows (as DebtEntry) and every person's new
// balance (the rows' people + keys). Unknown ids are skipped (a row the write removed).
func (s *Service) DebtEffects(ctx context.Context, userID uuid.UUID, ids []uuid.UUID, keys []string) ([]any, []any, error) {
	debts, persons := []any{}, []any{}
	views, err := loadViews(ctx, s.store.db, `WHERE pd.user_id = $1 AND pd.id = ANY($2) ORDER BY pd.date, pd.created_at, pd.id`,
		userID, uniqueIDs(ids))
	if err != nil {
		return nil, nil, err
	}
	all := append([]string{}, keys...)
	for _, v := range views {
		debts = append(debts, v)
		all = append(all, v.Key)
	}
	seen := map[string]bool{}
	for _, k := range all {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		b, err := s.balanceOfKey(ctx, s.store.db, userID, k)
		if err != nil {
			return nil, nil, err
		}
		persons = append(persons, b)
	}
	return debts, persons, nil
}
