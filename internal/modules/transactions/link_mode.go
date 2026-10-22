package transactions

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Link-mode switches on PUT /transactions/{id} (0.4.0-3 B3). A bill is shared one way at a time;
// switching drops the old way's data and writes the new one in the same DB transaction:
//   - leaving an event: the bill made from the row goes (its board splits are not brought back), a
//     copy just unlinks;
//   - leaving split: the row's split debts are deleted (linked partners get split_changed removed);
//   - leaving paid: the paid-by debt is deleted (the wallet takes the whole amount);
//   - then splits / paid_by / event from the request.
// Repayments are untouched (they belong to the person, not to the bill).

// ErrLinkModeInput — the new mode came without its data.
var ErrLinkModeInput = errors.New("link_mode needs its data: split → splits, paid → paid_by, event → event.project_id")

func reloadTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Transaction, error) {
	t, err := scanTx(tx.QueryRow(ctx, `SELECT `+txColumns+` FROM transactions WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTxNotFound
	}
	return t, err
}

// personKeysTx — the debt people a row touches: its splits, its paid-by debt, a debt entry it is the
// wallet side of, a repayment's person.
func personKeysTx(ctx context.Context, q shared.Querier, userID, txID uuid.UUID) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT `+shared.DebtKeySQL("d")+` FROM personal_debts d
		WHERE d.user_id = $1 AND (d.source_transaction_id = $2 OR d.paid_for_transaction_id = $2
		   OR d.id = (SELECT source_personal_debt_id FROM transactions WHERE id = $2))
		UNION
		SELECT `+shared.RepayKeySQL("t")+` FROM transactions t
		WHERE t.id = $2 AND t.user_id = $1 AND t.repay_person_name IS NOT NULL`, userID, txID)
	if err != nil {
		return nil, fmt.Errorf("row people: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func unionKeys(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range append(append([]string{}, a...), b...) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

type mode string

func currentMode(ctx context.Context, tx pgx.Tx, t *Transaction) (mode, bool, error) {
	splits, err := hasSplitsTx(ctx, tx, t.ID)
	if err != nil {
		return "", false, err
	}
	switch {
	case inEvent(t):
		return LinkEvent, splits, nil
	case splits:
		return LinkSplit, true, nil
	case t.PaidByAmount != nil:
		return LinkPaid, false, nil
	}
	return LinkPersonal, false, nil
}

// switchModeTx — the request's link_mode against the row: drops the old mode's data and checks the
// new one has its own. It answers the event to join when the row goes into one.
func (s *Service) switchModeTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, current *Transaction, req *UpdateRequest) (*EventTarget, error) {
	if req.LinkMode == nil {
		if req.Event != nil {
			return nil, ErrLinkModeConflict // an event goes with link_mode event
		}
		if req.Splits != nil {
			if current.UserID != userID {
				return nil, ErrSplitsAuthorOnly
			}
			if current.PaidByAmount != nil || inEvent(current) {
				return nil, ErrLinkModeConflict
			}
		}
		return nil, nil
	}
	if current.UserID != userID {
		return nil, ErrSplitsAuthorOnly
	}
	if current.SourcePersonalDebtID != nil || current.RepayPersonName != nil {
		return nil, ErrSystemTransactionImmutable
	}
	from, hasSplits, err := currentMode(ctx, tx, current)
	if err != nil {
		return nil, err
	}
	to := mode(*req.LinkMode)
	pb, pbSent := req.PaidByChange()

	// The new mode's data must come with it (or already be there), and only its data.
	switch to {
	case LinkSplit:
		if req.Splits == nil && from != LinkSplit {
			return nil, ErrLinkModeInput
		}
	case LinkPaid:
		if (pbSent && pb == nil) || (!pbSent && current.PaidByAmount == nil) {
			return nil, ErrLinkModeInput
		}
	case LinkEvent:
		if req.Event == nil && from != LinkEvent {
			return nil, ErrLinkModeInput
		}
		if current.Type != TypeExpense && current.Type != TypeIncome {
			return nil, ErrSplitsOnTransfer
		}
	}
	if (to != LinkSplit && req.Splits != nil && len(*req.Splits) > 0) ||
		(to != LinkPaid && pbSent && pb != nil) || (to != LinkEvent && req.Event != nil) {
		return nil, ErrLinkModeConflict
	}

	// Drop the old mode.
	moving := to == LinkEvent && req.Event != nil && (current.ProjectID == nil || *current.ProjectID != req.Event.ProjectID)
	if from == LinkEvent && (to != LinkEvent || moving) {
		if s.eventLinks == nil {
			return nil, ErrSplitsNotSupportedYet
		}
		if err := s.eventLinks.LeaveTx(ctx, tx, userID, current.ID); err != nil {
			return nil, err
		}
	}
	if to != LinkSplit && hasSplits && s.splitsEditor != nil {
		currency, err := splitCurrencyTx(ctx, tx, userID, current.AccountID)
		if err != nil {
			return nil, err
		}
		if err := s.splitsEditor(ctx, tx, current.ID, userID, string(current.Type), currency, current.Description, nil); err != nil {
			return nil, err
		}
	}
	if to != LinkPaid && current.PaidByAmount != nil {
		req.PaidBy = nil
		req.MarkPresent("paid_by")
	}
	if to == LinkEvent && (from != LinkEvent || moving) {
		return req.Event, nil
	}
	return nil, nil
}

func splitCurrencyTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, accountID *uuid.UUID) (string, error) {
	var currency string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE((SELECT currency FROM accounts WHERE id = $1),
		                (SELECT currency FROM users WHERE id = $2), 'THB')`, accountID, userID).Scan(&currency)
	if err != nil {
		return "", fmt.Errorf("currency: %w", err)
	}
	return currency, nil
}

// applySplitsTx — PUT splits: replaces the row's splits (as PUT /transactions/{id}/splits).
func (s *Service) applySplitsTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *Transaction, edits []SplitEdit) error {
	if s.splitsEditor == nil {
		return ErrSplitsNotSupportedYet
	}
	if row.PaidByAmount != nil && len(edits) > 0 {
		return ErrLinkModeConflict
	}
	var sum float64
	for _, e := range edits {
		sum += e.OwedAmount
	}
	if sum > row.Amount+0.005 {
		return ErrSplitsExceedAmount
	}
	currency, err := splitCurrencyTx(ctx, tx, userID, row.AccountID)
	if err != nil {
		return err
	}
	if edits, err = contactNamesTx(ctx, tx, userID, row.ID, edits); err != nil {
		return err
	}
	return s.splitsEditor(ctx, tx, row.ID, userID, string(row.Type), currency, row.Description, edits)
}

// previewResult — read the edit's result in the savepoint (sp), then the "before" in the outer DB
// transaction once the savepoint is rolled back.
func (s *Service) previewResult(ctx context.Context, outer, sp pgx.Tx, userID uuid.UUID, u *updated) (*PreviewResult, error) {
	rows, err := buildDetails(ctx, sp, userID, u.rows, true)
	if err != nil {
		return nil, err
	}
	out := &PreviewResult{Preview: true, Rows: rows, People: []PersonDelta{}, Wallets: []WalletDelta{}}
	afterNet := map[string]float64{}
	persons := map[string]shared.PersonRef{}
	if s.debtLinks != nil {
		for _, k := range u.personKeys {
			p, n, err := s.debtLinks.PersonNetTx(ctx, sp, userID, k)
			if err != nil {
				return nil, err
			}
			afterNet[k], persons[k] = n, p
		}
	}
	walletIDs := uniqueUUIDs(u.touched)
	afterBal, err := shared.LoadWalletBalances(ctx, sp, walletIDs)
	if err != nil {
		return nil, err
	}
	if err := sp.Rollback(ctx); err != nil {
		return nil, fmt.Errorf("rollback savepoint: %w", err)
	}
	for _, k := range u.personKeys {
		p, before, err := s.debtLinks.PersonNetTx(ctx, outer, userID, k)
		if err != nil {
			return nil, err
		}
		if persons[k].Name == "" {
			persons[k] = p
		}
		out.People = append(out.People, PersonDelta{Key: k, Person: persons[k], Before: before, After: afterNet[k],
			Delta: round2(afterNet[k] - before)})
	}
	beforeBal, err := shared.LoadWalletBalances(ctx, outer, walletIDs)
	if err != nil {
		return nil, err
	}
	before := map[uuid.UUID]float64{}
	for _, b := range beforeBal {
		before[b.AccountID] = b.Balance
	}
	for _, a := range afterBal {
		out.Wallets = append(out.Wallets, WalletDelta{AccountID: a.AccountID, Before: before[a.AccountID], After: a.Balance,
			Delta: round2(a.Balance - before[a.AccountID])})
	}
	return out, nil
}
