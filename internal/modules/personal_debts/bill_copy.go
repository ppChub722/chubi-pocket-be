package personal_debts

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Shared bills (0.4.1-2 S5): the person-book side of a member's copy of a bill, for the projects
// engine. A bill debt is a debt with a bill part (personal_debt_bills); the "คืนแล้ว" repayments
// point at the payback row made for the bill (source_project_transaction_id).

// GetEntryTx — one of my rows, read through tx (nil when it isn't mine / is gone).
func GetEntryTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Entry, error) {
	e, err := getEntry(ctx, tx, userID, id, true)
	if err == ErrEntryNotFound {
		return nil, nil
	}
	return e, err
}

// DeleteEntryTx deletes one of my rows and its own transaction (a "คืนแล้ว" repayment into a
// wallet); its bill parts go with it. Answers the wallet the transaction was on.
func (s *Service) DeleteEntryTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*uuid.UUID, error) {
	own, err := entryTxID(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	var wallet *uuid.UUID
	if own != nil {
		if wallet, err = s.txs.DeleteDebtTxInTx(ctx, tx, userID, *own); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM personal_debts WHERE id = $1 AND user_id = $2`, id, userID); err != nil {
		return nil, fmt.Errorf("delete entry: %w", err)
	}
	return wallet, nil
}

// SyncBillRowTx — a bill edit reached one of my rows: a debt that covers only this bill (a lump of
// one), or a "คืนแล้ว" repayment, takes the bill's date / description / note (and amount, for a
// repayment; a debt's amount is Σ its parts). A lump of many keeps its own text.
func (s *Service) SyncBillRowTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, amount *float64,
	date string, description, note *string) error {
	e, err := getEntry(ctx, tx, userID, id, true)
	if err != nil {
		return err
	}
	if e.Kind == KindDebt {
		var parts int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM personal_debt_bills WHERE debt_id = $1`, id).Scan(&parts); err != nil {
			return fmt.Errorf("bill parts: %w", err)
		}
		if parts > 1 {
			return nil
		}
		amount = nil
	}
	description, note = shared.CleanText(description), shared.CleanText(note)
	same := e.Date == date && sameText(e.Description, description) && sameText(e.Note, note) &&
		(amount == nil || round2(*amount) == e.Amount)
	if same {
		return nil
	}
	newAmount := e.Amount
	if amount != nil {
		newAmount = round2(*amount)
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debts SET amount = $2, date = $3::date, description = $4, note = $5,
		updated_by_user_id = user_id WHERE id = $1`, id, newAmount, date, description, note); err != nil {
		return fmt.Errorf("sync bill row: %w", err)
	}
	own, err := entryTxID(ctx, tx, id)
	if err != nil || own == nil {
		return err
	}
	var wallet *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT account_id FROM transactions WHERE id = $1`, *own).Scan(&wallet); err != nil {
		return fmt.Errorf("bill row tx wallet: %w", err)
	}
	_, err = s.txs.SyncDebtTxInTx(ctx, tx, userID, *own, transactions.DebtTxInput{
		Kind: e.Kind, Direction: e.Direction, AccountID: wallet, Amount: newAmount, Date: date,
		Description: description, Note: note,
	})
	return err
}

func sameText(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// RowEffects — the B2 answer for rows a bill write touched (read through q).
func (s *Service) RowEffects(ctx context.Context, q shared.Querier, userID uuid.UUID, debts, removed []uuid.UUID,
	keys []string) (shared.Effects, error) {
	return s.effects(ctx, q, userID, touch{debts: debts, removedDebts: removed, keys: keys})
}

// billRowGuardTx — 0.4.1-2 S9 / S11 on PUT /personal-debts/{id}: a debt of a shared bill (a bill
// part) or a "คืนแล้ว" repayment of one, its event still syncing, follows the bill: amount, date,
// direction, person, description, note and its transaction are the bill form's
// (transactions.SharedBillFieldError). paid / due_date / private_note stay mine; private_note lives
// only on such a row (transactions.ErrPrivateNoteNotShared). The private note is written here.
func billRowGuardTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, req *UpdateEntryRequest) error {
	var onBill, active bool
	err := tx.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM personal_debt_bills b WHERE b.debt_id = $1)
			OR EXISTS (SELECT 1 FROM personal_debts d JOIN project_transactions pb ON pb.id = d.source_project_transaction_id
				WHERE d.id = $1 AND d.kind = 'repayment' AND pb.paid_back_for_id IS NOT NULL),
			EXISTS (SELECT 1 FROM personal_debt_bills b JOIN project_transactions pt ON pt.id = b.pt_id
				JOIN scopes p ON p.id = pt.scope_id WHERE b.debt_id = $1 AND p.status = 'active')
			OR EXISTS (SELECT 1 FROM personal_debts d JOIN project_transactions pb ON pb.id = d.source_project_transaction_id
				JOIN scopes p ON p.id = pb.scope_id
				WHERE d.id = $1 AND d.kind = 'repayment' AND pb.paid_back_for_id IS NOT NULL AND p.status = 'active')`,
		id).Scan(&onBill, &active)
	if err != nil {
		return fmt.Errorf("bill row: %w", err)
	}
	noteSent := req.has("private_note")
	if noteSent && !onBill {
		return transactions.ErrPrivateNoteNotShared
	}
	if active {
		var fields []string
		for _, k := range []string{"direction", "amount", "date", "description", "note", "key", "person_name",
			"account_id", "create_transaction", "tag_ids"} {
			if req.has(k) {
				fields = append(fields, k)
			}
		}
		if len(fields) > 0 {
			return &transactions.SharedBillFieldError{Fields: fields}
		}
	}
	if !noteSent {
		return nil
	}
	note := req.PrivateNote
	if note != nil && len([]rune(*note)) > 500 {
		return transactions.ErrPrivateNoteTooLong
	}
	_, err = tx.Exec(ctx, `UPDATE personal_debts SET private_note = $3, updated_by_user_id = $2 WHERE id = $1 AND user_id = $2`,
		id, userID, shared.CleanText(note))
	if err != nil {
		return fmt.Errorf("private note: %w", err)
	}
	return nil
}
