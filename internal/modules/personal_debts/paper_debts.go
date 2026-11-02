package personal_debts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Debts made from papers (split / event bills). Every link from a debt to papers is a row of
// personal_debt_bills: one paper + the other person of the pair on it (+ my transaction of that
// paper). A split-bill claim makes a debt of one part; an event lump makes one debt of many parts.
// debt.amount = Σ parts. Two debts made from the same paper for the same pair, one in each book,
// are partners (counterpart_debt_id both ways): a repayment on one can be told to the other.

// SourcePaper — a debt made from a paper.
const SourcePaper = "paper"

// ErrBillAlreadyBooked — that paper + person is already in one of my debts.
var ErrBillAlreadyBooked = errors.New("this bill is already booked with that person")

func (s *Service) WithNotifications(n *notifications.Service) { s.notifs = n }

// tellPartnerTx — my repayment r with a person I share paper debts with: the partner gets
// debt_repaid. Their auto_accept_repayments on = the mirrored repayment is written in their book now
// (no wallet; they pick it later); off = the notice asks and the app opens their repayment sheet.
// No partner debt with that person = nothing to tell.
func (s *Service) tellPartnerTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, r *Entry) error {
	if s.notifs == nil {
		return nil
	}
	var partnerUser, partnerDebt uuid.UUID
	err := tx.QueryRow(ctx, `SELECT p.user_id, p.id FROM personal_debts d
		JOIN personal_debts p ON p.id = d.counterpart_debt_id
		WHERE d.user_id = $1 AND d.kind = 'debt' AND `+shared.DebtKeySQL("d")+` = $2
		ORDER BY d.date DESC, d.created_at DESC LIMIT 1`, userID, r.Key()).Scan(&partnerUser, &partnerDebt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("repayment partner: %w", err)
	}
	return s.TellRepaidTx(ctx, tx, userID, partnerUser, partnerDebt, r.Amount, r.Date, r.Description)
}

// TellRepaidTx — actor settled amount of the pair behind theirDebt (the other user's debt): that
// user gets debt_repaid, with the repayment already in their book when auto_accept_repayments is on
// (no wallet: they pick it later). Their debt's direction says which way the money went.
func (s *Service) TellRepaidTx(ctx context.Context, tx pgx.Tx, actor, theirUser, theirDebt uuid.UUID, amount float64,
	date string, description *string) error {
	if s.notifs == nil || theirUser == actor {
		return nil
	}
	theirs, err := getEntry(ctx, tx, theirUser, theirDebt, false)
	if err != nil {
		return err
	}
	// their debt "paid" = they paid for the actor: the actor's money comes in (received)
	kind, direction := RepayReceive, DirectionReceived
	if theirs.Direction == DirectionReceived {
		kind, direction = RepayPay, DirectionPaid
	}
	p := notifications.DebtRepaidPayload{
		DebtID: theirDebt, Key: theirs.Key(), PersonName: theirs.PersonName, Amount: amount,
		Direction: direction, Date: date, Description: description,
	}
	st, err := s.notifs.SettingsTx(ctx, tx, theirUser)
	if err != nil {
		return err
	}
	if st.AutoAcceptRepayments {
		no := false
		mine, _, err := s.createRepaymentTx(ctx, tx, theirUser, theirs.Key(), RepaymentRequest{
			Kind: kind, Amount: amount, Date: &date, Description: description,
			CreateTransaction: &no, PersonName: &theirs.PersonName,
		})
		if err != nil {
			return err
		}
		p.RepaymentID = &mine.ID
	}
	return s.notifs.DispatchDebtRepaid(ctx, tx, theirUser, actor, p)
}

// PaperPart — one paper's part of a debt.
type PaperPart struct {
	ID            uuid.UUID
	DebtID        uuid.UUID
	UserID        uuid.UUID
	PaperID       uuid.UUID
	PersonID      uuid.UUID // the other person of the pair on that paper
	TransactionID *uuid.UUID
	Amount        float64
}

// A part whose person was taken off the paper reads as uuid.Nil: nothing is owed to it any more, so
// it shows out of sync until "อัปเดต" drops it.
const paperPartColumns = `id, debt_id, user_id, split_transaction_id,
	COALESCE(split_person_id, '00000000-0000-0000-0000-000000000000'::uuid), transaction_id, amount::float8`

func scanPaperPart(row pgx.Row) (*PaperPart, error) {
	var p PaperPart
	if err := row.Scan(&p.ID, &p.DebtID, &p.UserID, &p.PaperID, &p.PersonID, &p.TransactionID, &p.Amount); err != nil {
		return nil, err
	}
	return &p, nil
}

func queryPaperParts(ctx context.Context, q shared.Querier, tail string, args ...any) ([]PaperPart, error) {
	rows, err := q.Query(ctx, `SELECT `+paperPartColumns+` FROM personal_debt_bills `+tail, args...)
	if err != nil {
		return nil, fmt.Errorf("paper parts: %w", err)
	}
	defer rows.Close()
	out := []PaperPart{}
	for rows.Next() {
		p, err := scanPaperPart(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// PaperPartsTx — every user's parts of a paper.
func PaperPartsTx(ctx context.Context, q shared.Querier, paperID uuid.UUID) ([]PaperPart, error) {
	return queryPaperParts(ctx, q, `WHERE split_transaction_id = $1 ORDER BY user_id, created_at, id`, paperID)
}

// PaperPartsOfDebtTx — the papers a debt covers.
func PaperPartsOfDebtTx(ctx context.Context, q shared.Querier, debtID uuid.UUID) ([]PaperPart, error) {
	return queryPaperParts(ctx, q, `WHERE debt_id = $1 ORDER BY created_at, id`, debtID)
}

// MyPaperPartsTx — my parts on any of these papers.
func MyPaperPartsTx(ctx context.Context, q shared.Querier, userID uuid.UUID, paperIDs []uuid.UUID) ([]PaperPart, error) {
	return queryPaperParts(ctx, q, `WHERE user_id = $1 AND split_transaction_id = ANY($2) ORDER BY created_at, id`, userID, paperIDs)
}

// PaperDebtInput — a debt to make from papers.
type PaperDebtInput struct {
	// IOwe — I owe them (direction received); else they owe me (paid).
	IOwe        bool
	ContactID   *uuid.UUID
	PersonName  string
	Date        string
	Description *string
	ProjectID   *uuid.UUID // an event lump
	Parts       []PaperPart
}

// CreatePaperDebtTx — a debt of Σ parts (ErrBillAlreadyBooked when a part is in a debt already).
func (s *Service) CreatePaperDebtTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, in PaperDebtInput) (*Entry, error) {
	total := 0.0
	for _, p := range in.Parts {
		total += p.Amount
	}
	dir := DirectionPaid
	if in.IOwe {
		dir = DirectionReceived
	}
	kind := SourcePaper
	e, err := insertEntryTx(ctx, tx, &Entry{
		UserID: userID, Kind: KindDebt, Direction: dir, ContactID: in.ContactID, PersonName: in.PersonName,
		ProjectID: in.ProjectID, Amount: round2(total), Date: in.Date, SourceKind: &kind, Description: in.Description,
	})
	if err != nil {
		return nil, err
	}
	for _, p := range in.Parts {
		tag, err := tx.Exec(ctx, `INSERT INTO personal_debt_bills (debt_id, user_id, split_transaction_id, split_person_id, transaction_id, amount)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (user_id, split_transaction_id, split_person_id) DO NOTHING`,
			e.ID, userID, p.PaperID, p.PersonID, p.TransactionID, round2(p.Amount))
		if err != nil {
			return nil, fmt.Errorf("paper part: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil, ErrBillAlreadyBooked
		}
	}
	return e, nil
}

// LinkPaperPartTx — a row I wrote myself (a repayment of a payback paper) counts as booking that
// paper + person.
func LinkPaperPartTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID, p PaperPart) error {
	tag, err := tx.Exec(ctx, `INSERT INTO personal_debt_bills (debt_id, user_id, split_transaction_id, split_person_id, transaction_id, amount)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (user_id, split_transaction_id, split_person_id) DO NOTHING`,
		entryID, userID, p.PaperID, p.PersonID, p.TransactionID, round2(p.Amount))
	if err != nil {
		return fmt.Errorf("paper part: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrBillAlreadyBooked
	}
	_, err = tx.Exec(ctx, `UPDATE personal_debts SET source_kind = 'paper' WHERE id = $1`, entryID)
	return err
}

// SetPaperPartAmountTx — a paper edit reached my part: the part and its debt follow.
func (s *Service) SetPaperPartAmountTx(ctx context.Context, tx pgx.Tx, userID, partID uuid.UUID, amount float64) error {
	var debtID uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE personal_debt_bills SET amount = $3 WHERE id = $1 AND user_id = $2 RETURNING debt_id`,
		partID, userID, round2(amount)).Scan(&debtID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrEntryNotFound
	}
	if err != nil {
		return fmt.Errorf("paper part amount: %w", err)
	}
	return s.debtFromPartsTx(ctx, tx, userID, debtID)
}

// DropPaperPartTx — a part leaves its debt (the pair is gone from the paper); the debt shrinks.
func (s *Service) DropPaperPartTx(ctx context.Context, tx pgx.Tx, userID, partID uuid.UUID) (uuid.UUID, error) {
	var debtID uuid.UUID
	err := tx.QueryRow(ctx, `DELETE FROM personal_debt_bills WHERE id = $1 AND user_id = $2 RETURNING debt_id`,
		partID, userID).Scan(&debtID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrEntryNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("drop paper part: %w", err)
	}
	return debtID, s.debtFromPartsTx(ctx, tx, userID, debtID)
}

// UnlinkPaperDebtTx — the debt stops following its papers: a plain debt (same amount) from now on.
func (s *Service) UnlinkPaperDebtTx(ctx context.Context, tx pgx.Tx, userID, debtID uuid.UUID) error {
	if _, err := getEntry(ctx, tx, userID, debtID, true); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM personal_debt_bills WHERE debt_id = $1 AND user_id = $2`, debtID, userID); err != nil {
		return fmt.Errorf("unlink paper debt: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debts SET source_kind = NULL, project_id = NULL, counterpart_debt_id = NULL,
		updated_by_user_id = $2 WHERE id = $1`, debtID, userID); err != nil {
		return fmt.Errorf("plain debt: %w", err)
	}
	_, err := tx.Exec(ctx, `UPDATE personal_debts SET counterpart_debt_id = NULL WHERE counterpart_debt_id = $1`, debtID)
	return err
}

// SetPartnersTx — two debts of one pair, one in each book, become partners.
func SetPartnersTx(ctx context.Context, tx pgx.Tx, a, b uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE personal_debts SET counterpart_debt_id = CASE id WHEN $1 THEN $2::uuid ELSE $1::uuid END
		WHERE id IN ($1, $2)`, a, b); err != nil {
		return fmt.Errorf("partners: %w", err)
	}
	return nil
}

// debtFromPartsTx sets a debt to Σ its parts (its own transaction follows). No part left → the debt
// keeps its amount and becomes plain.
func (s *Service) debtFromPartsTx(ctx context.Context, tx pgx.Tx, userID, debtID uuid.UUID) error {
	var (
		n     int
		total float64
	)
	if err := tx.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(amount), 0)::float8 FROM personal_debt_bills WHERE debt_id = $1`,
		debtID).Scan(&n, &total); err != nil {
		return fmt.Errorf("paper parts sum: %w", err)
	}
	e, err := getEntry(ctx, tx, userID, debtID, true)
	if err != nil {
		return err
	}
	if n == 0 {
		_, err := tx.Exec(ctx, `UPDATE personal_debts SET source_kind = NULL, updated_by_user_id = $2 WHERE id = $1`, debtID, userID)
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

// PrivateNoteChange — the private note sent (nil = clear) and whether it was sent.
func (r *UpdateEntryRequest) PrivateNoteChange() (*string, bool) {
	return r.PrivateNote, r.has("private_note")
}

// setDebtPrivateNoteTx — my private note, only on a debt made from a paper.
func setDebtPrivateNoteTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, e *Entry, note *string) error {
	if e.SourceKind == nil || *e.SourceKind != SourcePaper {
		return transactions.ErrPrivateNoteNotShared
	}
	if note != nil && len([]rune(*note)) > 500 {
		return transactions.ErrPrivateNoteTooLong
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debts SET private_note = $3, updated_by_user_id = $2
		WHERE id = $1 AND user_id = $2`, e.ID, userID, shared.CleanText(note)); err != nil {
		return fmt.Errorf("private note: %w", err)
	}
	return nil
}

// GetEntryTx — one of my rows, read through tx (nil when it isn't mine / is gone).
func GetEntryTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Entry, error) {
	e, err := getEntry(ctx, tx, userID, id, true)
	if err == ErrEntryNotFound {
		return nil, nil
	}
	return e, err
}

// DeleteEntryTx deletes one of my rows and its own transaction; its paper parts go with it.
// Answers the wallet the transaction was on.
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

// RowEffects — the B2 answer for rows a paper write touched (read through q).
func (s *Service) RowEffects(ctx context.Context, q shared.Querier, userID uuid.UUID, debts, removed []uuid.UUID,
	keys []string) (shared.Effects, error) {
	return s.effects(ctx, q, userID, touch{debts: debts, removedDebts: removed, keys: keys})
}

// PersonNetTx — a person's balance as q sees it (a write's preview reads it inside and outside its
// savepoint); 0 and the bare key's name when they have no lines.
func (s *Service) PersonNetTx(ctx context.Context, q shared.Querier, userID uuid.UUID, key string) (shared.PersonRef, float64, error) {
	k, err := shared.ParseDebtKey(key)
	if err != nil {
		return shared.PersonRef{}, 0, err
	}
	p, found, err := s.store.personBalance(ctx, q, userID, k)
	if err != nil {
		return shared.PersonRef{}, 0, err
	}
	if !found {
		return shared.PersonRef{Name: k.Name, ContactID: k.ContactID}, 0, nil
	}
	return p.Person, p.Net, nil
}
