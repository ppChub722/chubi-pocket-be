package transactions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Shared bills (0.4.1-2 S5): every member holds a copy of a bill in their book. The projects engine
// writes a member's copy inside the bill's DB transaction; these are its transaction-side hooks.

var (
	// ErrPrivateNoteTooLong — private_note over 500 characters (400 VALIDATION_ERROR).
	ErrPrivateNoteTooLong = apierr.Invalid("private_note: at most 500 characters", "private_note", apierr.FieldTooLong, "max", 500)
	// ErrPrivateNoteNotShared — private_note only lives on a copy of a shared bill (S11).
	ErrPrivateNoteNotShared = errors.New("private_note is only for a copy of a shared bill")
)

// SharedBillFieldError — S9: a field of a connected copy that belongs to the bill (edit it in the
// bill form). 409 SHARED_BILL_FIELD, details.fields.
type SharedBillFieldError struct{ Fields []string }

func (e *SharedBillFieldError) Error() string {
	return "these fields follow the shared bill; edit the bill: " + strings.Join(e.Fields, ", ")
}

// UpdateCopyInTx re-syncs a member's copy (amount / date / description / note, the wallet rule,
// the editor's own category / tags). The row is read through tx: it may be new in this DB tx.
func (s *Service) UpdateCopyInTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, req UpdateRequest) error {
	current, err := reloadTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if current.UserID != userID {
		return ErrTxNotFound
	}
	if _, _, err := s.updateSingleInTx(ctx, tx, userID, current, req); err != nil {
		return err
	}
	return s.replaceTagsIfSent(ctx, tx, userID, id, req)
}

// CopyRowTx — a member's copy as the engine compares it (read through tx).
func CopyRowTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Transaction, error) {
	return reloadTx(ctx, tx, id)
}

// SetPrivateNoteTx — 0.4.1-2 S11: my private note on a copy of a shared bill (blank = none).
func SetPrivateNoteTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, note *string) error {
	if note != nil && len([]rune(*note)) > 500 {
		return ErrPrivateNoteTooLong
	}
	if _, err := tx.Exec(ctx, `UPDATE transactions SET private_note = $3, updated_by_user_id = $2
		WHERE id = $1 AND user_id = $2`, id, userID, shared.CleanText(note)); err != nil {
		return fmt.Errorf("private note: %w", err)
	}
	return nil
}

// loadPendingBills — 0.4.1-2 S8: the bills an unanswered notice waits on for me ("อัปเดตตาม": a
// shared_bill_created / project_tx_changed that my switch did not apply).
func loadPendingBills(ctx context.Context, q shared.Querier, userID uuid.UUID, billIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(billIDs) == 0 {
		return out, nil
	}
	ids := make([]string, len(billIDs))
	for i, id := range billIDs {
		ids[i] = id.String()
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT payload->>'project_transaction_id' FROM notifications
		WHERE recipient_user_id = $1 AND actioned_at IS NULL AND dismissed_at IS NULL
		  AND type IN ('shared_bill_created', 'project_tx_changed')
		  AND COALESCE((payload->>'applied')::boolean, FALSE) = FALSE
		  AND payload->>'project_transaction_id' = ANY($2)`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("pending bills: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if id, err := uuid.Parse(s); err == nil {
			out[id] = true
		}
	}
	return out, rows.Err()
}

// sharedCopyGuardTx — 0.4.1-2 S9 / S11 on PUT /transactions/{id}. A connected copy of a shared bill
// (its bill's event not completed) takes only my own fields: category, tags, private note, and the
// wallet when the money is mine to place (I paid, or I paid back; not while "จ่ายโดย X"). The bill's
// values are edited in the bill form → SharedBillFieldError. The private note is written here (only on
// a copy of a shared bill: ErrPrivateNoteNotShared otherwise).
func sharedCopyGuardTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, current *Transaction, req *UpdateRequest) error {
	note, noteSent := req.PrivateNoteChange()
	if current.SourceProjectTransactionID == nil || current.UserID != userID {
		if noteSent {
			return ErrPrivateNoteNotShared
		}
		return nil
	}
	var (
		status   string
		walletOK bool
	)
	err := tx.QueryRow(ctx, `SELECT p.status,
			COALESCE(b.transaction_member_id = pm.id OR EXISTS (SELECT 1 FROM project_transactions pb
				WHERE pb.paid_back_for_id = b.id AND pb.transaction_member_id = pm.id AND pb.parent_project_transaction_id IS NULL), FALSE)
		FROM project_transactions b
		JOIN projects p ON p.id = b.project_id
		LEFT JOIN project_members pm ON pm.project_id = b.project_id AND pm.user_id = $2
		WHERE b.id = (SELECT split_transaction_id FROM transactions WHERE id = $1)`, current.ID, userID).Scan(&status, &walletOK)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("shared copy: %w", err)
	}
	if status != "active" { // a completed event's copy is a plain transaction again
		if noteSent {
			return SetPrivateNoteTx(ctx, tx, userID, current.ID, note)
		}
		return nil
	}
	var fields []string
	add := func(f string, set bool) {
		if set {
			fields = append(fields, f)
		}
	}
	_, descSent := req.DescriptionChange()
	_, noteFieldSent := req.NoteChange()
	_, pbSent := req.PaidByChange()
	_, accSent := req.AccountIDChange()
	add("amount", req.Amount != nil)
	add("date", req.Date != nil)
	add("description", descSent)
	add("note", noteFieldSent)
	add("paid_by", pbSent)
	add("splits", req.Splits != nil)
	add("link_mode", req.LinkMode != nil)
	add("event", req.Event != nil)
	add("apply_to_event", req.ApplyToEvent || req.AlsoUpdateLink)
	add("account_id", accSent && !walletOK)
	if len(fields) > 0 {
		return &SharedBillFieldError{Fields: fields}
	}
	if noteSent {
		return SetPrivateNoteTx(ctx, tx, userID, current.ID, note)
	}
	return nil
}

// SetCopyPaidByTx — 0.4.1-2 multi-payer: the part of my copy other payers paid (nil = none). The
// wallet moves only the rest; no debt is made here (the bill engine writes my debts from the pairs).
// Answers the wallet it moved (nil when nothing changed).
func (s *Service) SetCopyPaidByTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, paidBy *float64) (*uuid.UUID, error) {
	cur, err := reloadTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	same := (cur.PaidByAmount == nil && paidBy == nil) ||
		(cur.PaidByAmount != nil && paidBy != nil && round2(*cur.PaidByAmount) == round2(*paidBy))
	if same {
		return nil, nil
	}
	oldCash := cur.Cash()
	if _, err := tx.Exec(ctx, `UPDATE transactions SET paid_by_amount = $2, updated_by_user_id = $3 WHERE id = $1`,
		id, paidBy, userID); err != nil {
		return nil, fmt.Errorf("copy paid by: %w", err)
	}
	cur.PaidByAmount = paidBy
	if cur.AccountID == nil {
		return nil, nil
	}
	if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*cur.AccountID}); err != nil {
		return nil, err
	}
	delta := signedDelta(cur.Type, cur.Cash(), false) - signedDelta(cur.Type, oldCash, false)
	if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *cur.AccountID, delta); err != nil {
		return nil, err
	}
	return cur.AccountID, nil
}
