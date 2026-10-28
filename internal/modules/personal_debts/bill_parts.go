package personal_debts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// Bill parts (0.4.1-2 V5 / S10, migration 100): a debt covers one bill (a lump of one) or many (a
// lump, "claim ก้อน"). One part = one bill + the other person of that bill (member) + my
// transaction of that bill (the "จ่ายโดย X" copy, or my origin when I paid). debt.amount = Σ parts.

// ErrBillAlreadyBooked — the bill + person is already in one of my debts (X7).
var ErrBillAlreadyBooked = errors.New("this bill is already booked with that person")

// ErrUseClaim — bill_id / event_id on a person-book create are retired (0.4.1-2 V8 / V9): bills are
// claimed through POST /projects/{id}/claim (once per bill + person).
var ErrUseClaim = errors.New("bill_id / event_id are retired here: claim the bills with POST /projects/{id}/claim")

// BillPart — one bill's part of a debt.
type BillPart struct {
	ID            uuid.UUID
	DebtID        uuid.UUID
	PtID          uuid.UUID
	MemberID      uuid.UUID
	TransactionID *uuid.UUID
	Amount        float64
}

// BillPartInput — a bill to add to a debt.
type BillPartInput struct {
	PtID          uuid.UUID // the bill (parent row)
	MemberID      uuid.UUID // the other person of the pair on that bill
	TransactionID *uuid.UUID
	Amount        float64
}

const billPartColumns = `id, debt_id, pt_id, member_id, transaction_id, amount::float8`

func scanBillPart(row pgx.Row) (*BillPart, error) {
	var p BillPart
	if err := row.Scan(&p.ID, &p.DebtID, &p.PtID, &p.MemberID, &p.TransactionID, &p.Amount); err != nil {
		return nil, err
	}
	return &p, nil
}

// BillPartTx — my part for a bill + person (nil when that pair isn't booked).
func BillPartTx(ctx context.Context, tx pgx.Tx, userID, ptID, memberID uuid.UUID) (*BillPart, error) {
	p, err := scanBillPart(tx.QueryRow(ctx, `SELECT `+billPartColumns+` FROM personal_debt_bills
		WHERE user_id = $1 AND pt_id = $2 AND member_id = $3`, userID, ptID, memberID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bill part: %w", err)
	}
	return p, nil
}

// BillPartsOfBillTx — every user's parts of a bill (the fan-out of a bill edit / delete).
func BillPartsOfBillTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID) ([]BillPart, error) {
	return queryBillParts(ctx, tx, `WHERE pt_id = $1 ORDER BY user_id, member_id`, ptID)
}

// BillPartsOfDebtTx — the bills a debt covers.
func BillPartsOfDebtTx(ctx context.Context, tx pgx.Tx, debtID uuid.UUID) ([]BillPart, error) {
	return queryBillParts(ctx, tx, `WHERE debt_id = $1 ORDER BY created_at, id`, debtID)
}

func queryBillParts(ctx context.Context, tx pgx.Tx, tail string, args ...any) ([]BillPart, error) {
	rows, err := tx.Query(ctx, `SELECT `+billPartColumns+` FROM personal_debt_bills `+tail, args...)
	if err != nil {
		return nil, fmt.Errorf("bill parts: %w", err)
	}
	defer rows.Close()
	out := []BillPart{}
	for rows.Next() {
		p, err := scanBillPart(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// LinkBillPartTx adds a bill to my debt (ErrBillAlreadyBooked when that pair is booked already) and
// sets the debt to Σ its parts.
func (s *Service) LinkBillPartTx(ctx context.Context, tx pgx.Tx, userID, debtID uuid.UUID, in BillPartInput) error {
	if in.Amount < 0 {
		return invalid("a bill part can't be negative")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO personal_debt_bills (debt_id, user_id, pt_id, member_id, transaction_id, amount)
		SELECT pd.id, pd.user_id, $3, $4, $5, $6 FROM personal_debts pd
		WHERE pd.id = $1 AND pd.user_id = $2 AND pd.kind = 'debt'
		ON CONFLICT (user_id, pt_id, member_id) DO NOTHING`,
		debtID, userID, in.PtID, in.MemberID, in.TransactionID, round2(in.Amount))
	if err != nil {
		return fmt.Errorf("link bill part: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := getEntry(ctx, tx, userID, debtID, false); err != nil {
			return err
		}
		return ErrBillAlreadyBooked
	}
	return s.debtFromPartsTx(ctx, tx, userID, debtID)
}

// SetBillPartAmountTx — a bill edit reached my part (auto sync, or "อัปเดตตาม"): the part and its
// debt follow.
func (s *Service) SetBillPartAmountTx(ctx context.Context, tx pgx.Tx, userID, partID uuid.UUID, amount float64) error {
	var debtID uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE personal_debt_bills SET amount = $3 WHERE id = $1 AND user_id = $2 RETURNING debt_id`,
		partID, userID, round2(amount)).Scan(&debtID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrEntryNotFound
	}
	if err != nil {
		return fmt.Errorf("bill part amount: %w", err)
	}
	return s.debtFromPartsTx(ctx, tx, userID, debtID)
}

// SetBillPartTransactionTx — the part's transaction (my copy / origin of that bill); nil = none.
func SetBillPartTransactionTx(ctx context.Context, tx pgx.Tx, userID, partID uuid.UUID, txID *uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE personal_debt_bills SET transaction_id = $3 WHERE id = $1 AND user_id = $2`,
		partID, userID, txID); err != nil {
		return fmt.Errorf("bill part tx: %w", err)
	}
	return nil
}

// UnlinkBillPartTx takes a bill out of its debt. The debt shrinks to the rest; with no part left it
// stays as a plain debt (its last amount, no event source). Answers the debt.
func (s *Service) UnlinkBillPartTx(ctx context.Context, tx pgx.Tx, userID, partID uuid.UUID) (uuid.UUID, error) {
	var debtID uuid.UUID
	err := tx.QueryRow(ctx, `DELETE FROM personal_debt_bills WHERE id = $1 AND user_id = $2 RETURNING debt_id`,
		partID, userID).Scan(&debtID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrEntryNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("unlink bill part: %w", err)
	}
	return debtID, s.debtFromPartsTx(ctx, tx, userID, debtID)
}

// debtFromPartsTx sets a debt to Σ its parts (its own transaction follows). No part left → the debt
// keeps its amount and becomes plain (no event source).
func (s *Service) debtFromPartsTx(ctx context.Context, tx pgx.Tx, userID, debtID uuid.UUID) error {
	var (
		n     int
		total float64
	)
	if err := tx.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(amount), 0)::float8 FROM personal_debt_bills WHERE debt_id = $1`,
		debtID).Scan(&n, &total); err != nil {
		return fmt.Errorf("bill parts sum: %w", err)
	}
	e, err := getEntry(ctx, tx, userID, debtID, true)
	if err != nil {
		return err
	}
	if n == 0 {
		_, err := tx.Exec(ctx, `UPDATE personal_debts SET source_kind = NULL, source_project_transaction_id = NULL,
			updated_by_user_id = $2 WHERE id = $1 AND source_kind = 'event'`, debtID, userID)
		if err != nil {
			return fmt.Errorf("plain debt: %w", err)
		}
		return nil
	}
	total = round2(total)
	if total == e.Amount {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debts SET amount = $2, updated_by_user_id = $3 WHERE id = $1`,
		debtID, total, userID); err != nil {
		return fmt.Errorf("debt from parts: %w", err)
	}
	own, err := entryTxID(ctx, tx, debtID)
	if err != nil || own == nil {
		return err
	}
	var wallet *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT account_id FROM transactions WHERE id = $1`, *own).Scan(&wallet); err != nil {
		return fmt.Errorf("debt tx wallet: %w", err)
	}
	_, err = s.txs.SyncDebtTxInTx(ctx, tx, userID, *own, transactions.DebtTxInput{
		Kind: e.Kind, Direction: e.Direction, AccountID: wallet, Amount: total, Date: e.Date,
		Description: e.Description, Note: e.Note,
	})
	return err
}
