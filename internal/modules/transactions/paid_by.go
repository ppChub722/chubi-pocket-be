package transactions

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Paid by someone else (0.4.0-3): a bill of 123 where Lee paid 13 is ONE expense of 123 (my
// consumption) with paid_by_amount 13; the wallet moves 110 and the same save writes the debt
// "I owe Lee 13" (personal_debts.paid_for_transaction_id → this row). The debt side lives in
// personal_debts (DebtLinks.SetPaidByTx / PaidByDeletingTx).
// On an income (0.4.1-0, "รับแทน") Lee RECEIVED 13 of my 123 for me: the income counts 123,
// the wallet moves 110 and the debt is "Lee owes me 13".

var (
	ErrPaidByExpenseOnly    = errors.New("paid_by is for an expense or an income")
	ErrPaidByExceedsAmount  = errors.New("paid_by.amount is more than the bill")
	ErrPaidByPersonRequired = errors.New("paid_by needs a contact_id or a person_name")
	ErrLinkModeConflict     = errors.New("a bill is shared one way at a time: splits, paid_by or an event")
)

const paidByEps = 0.005

// PaidBySourceMe — the only paid_by source since 0.4.1-2 S16 (the partner mirror is gone).
const PaidBySourceMe = "me"

// paidByDebt — the debt a paid-by row carries.
type paidByDebt struct {
	ID         uuid.UUID
	SourceKind *string
	ContactID  *uuid.UUID
	Name       string
	Amount     float64
}

func loadPaidByDebtTx(ctx context.Context, q pgx.Tx, txID uuid.UUID) (*paidByDebt, error) {
	var d paidByDebt
	err := q.QueryRow(ctx, `SELECT id, source_kind, counterparty_contact_id, counterparty_person_name, amount
		FROM personal_debts WHERE paid_for_transaction_id = $1`, txID).
		Scan(&d.ID, &d.SourceKind, &d.ContactID, &d.Name, &d.Amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("paid-by debt: %w", err)
	}
	return &d, nil
}

func checkPaidByInput(t TxType, in *PaidByInput) error {
	if t != TypeExpense && t != TypeIncome {
		return ErrPaidByExpenseOnly
	}
	if in.ContactID == nil && strings.TrimSpace(in.PersonName) == "" {
		return ErrPaidByPersonRequired
	}
	return nil
}

// hasSplitsTx — the row carries splits (debts others owe me on it).
func hasSplitsTx(ctx context.Context, q pgx.Tx, txID uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM personal_debts WHERE source_transaction_id = $1)`, txID).Scan(&ok)
	return ok, err
}

// inEvent — the row is an event bill or a copy of one.
func inEvent(t *Transaction) bool {
	return t.ProjectID != nil || t.SourceProjectTransactionID != nil
}

// paidByPlan — the paid_by part an update leaves on the row.
type paidByPlan struct {
	existing *paidByDebt
	sent     bool
	input    *PaidByInput // what the debt becomes (nil = no paid_by)
	amount   *float64     // the row's new paid_by_amount
}

// planPaidBy checks an update's paid_by against the row and works out the new paid_by part. It may
// drop the wallet (they paid it all: nothing leaves a wallet).
func (s *Service) planPaidBy(ctx context.Context, tx pgx.Tx, current *Transaction, req *UpdateRequest) (*paidByPlan, error) {
	p := &paidByPlan{amount: current.PaidByAmount}
	in, sent := req.PaidByChange()
	if current.PaidByAmount == nil && !sent {
		return p, nil
	}
	var err error
	if p.existing, err = loadPaidByDebtTx(ctx, tx, current.ID); err != nil {
		return nil, err
	}
	p.sent = sent
	if sent {
		if in == nil {
			p.amount = nil
		} else {
			if err := checkPaidByInput(current.Type, in); err != nil {
				return nil, err
			}
			if inEvent(current) {
				return nil, ErrLinkModeConflict
			}
			splits, err := hasSplitsTx(ctx, tx, current.ID)
			if err != nil {
				return nil, err
			}
			if splits {
				return nil, ErrLinkModeConflict
			}
			a := in.Amount
			p.amount, p.input = &a, in
		}
	} else if p.existing != nil {
		p.input = &PaidByInput{ContactID: p.existing.ContactID, PersonName: p.existing.Name, Amount: *current.PaidByAmount}
	}
	if p.amount == nil {
		return p, nil
	}
	amount := current.Amount
	if req.Amount != nil {
		amount = *req.Amount
	}
	if *p.amount > amount+paidByEps {
		return nil, ErrPaidByExceedsAmount
	}
	if math.Abs(*p.amount-amount) < paidByEps {
		// They paid it all: no wallet.
		req.AccountID = nil
		req.MarkPresent("account_id")
	}
	return p, nil
}

// syncPaidByTx writes the debt side after the row is saved; it returns the person's key ("" when the
// row has no paid_by now and had none).
func (s *Service) syncPaidByTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *Transaction, p *paidByPlan) (string, error) {
	if p == nil || (p.existing == nil && p.input == nil) {
		return "", nil
	}
	if s.debtLinks == nil {
		return "", ErrSplitsNotSupportedYet
	}
	if p.input != nil {
		p.input.Amount = *row.PaidByAmount
	}
	key, err := s.debtLinks.SetPaidByTx(ctx, tx, userID, row, p.input)
	if err != nil {
		return "", err
	}
	if key == "" && p.existing != nil {
		key = shared.DebtKey(p.existing.ContactID, p.existing.Name)
	}
	return key, nil
}
