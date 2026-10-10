package projects

// Taking a transaction out of its event (owner 2026-10-10). The personal
// row is never deleted — it goes back to being a loose transaction.
//
//   - the tx is its board row's origin (I recorded the row and I'm its
//     actor: a pulled bill, or my own copy of my own row) → the board row
//     and its split children are deleted, and every personal row pointing
//     at them (mine + other members' copies) is unlinked. A copy is just a
//     copy: it stays in its owner's book as a plain loose transaction.
//   - otherwise (my copy of someone else's row) → only my row is unlinked;
//     the board row stays.
//
// Removal is always allowed (no project lock / membership check); only the
// transaction's author can do it.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// ErrBillNotInProject — the transaction isn't in this project
// (404 TX_NOT_IN_PROJECT).
var ErrBillNotInProject = errors.New("transaction is not in this project")

// RemoveBill implements DELETE /v1/projects/:id/bills/:transaction_id.
func (s *Service) RemoveBill(ctx context.Context, callerUserID, projectID, txID uuid.UUID) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		ownerID uuid.UUID
		inProj  *uuid.UUID
	)
	err = tx.QueryRow(ctx, `SELECT user_id, project_id FROM transactions WHERE id = $1 FOR UPDATE`,
		txID).Scan(&ownerID, &inProj)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && ownerID != callerUserID) {
		return ErrQuickTxNotFound // not-owned = not-found (no info leak)
	}
	if err != nil {
		return fmt.Errorf("load bill %s: %w", txID, err)
	}
	if inProj == nil || *inProj != projectID {
		return ErrBillNotInProject
	}
	if _, err := s.detachBillTx(ctx, tx, callerUserID, txID, true); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// detachBillTx takes the caller's transaction txID out of its project (see
// the file comment). When txID is its board row's origin, the row's member
// splits leave the board with it: restore = true puts them back on txID as
// personal splits (a removal); false hands them back for the caller to
// carry to another board (a move). Returns them (nil when txID was only a
// copy, so nothing moved).
func (s *Service) detachBillTx(
	ctx context.Context, tx pgx.Tx, callerUserID, txID uuid.UUID, restore bool,
) ([]quickDebt, error) {
	var (
		projectID *uuid.UUID
		sourcePT  *uuid.UUID
	)
	if err := tx.QueryRow(ctx, `
		SELECT project_id, source_project_transaction_id FROM transactions
		WHERE id = $1 AND user_id = $2`, txID, callerUserID).Scan(&projectID, &sourcePT); err != nil {
		return nil, fmt.Errorf("load bill %s: %w", txID, err)
	}

	// Personal splits pulled in before splits moved onto the board (old
	// data) only drop their event tag.
	if _, err := tx.Exec(ctx, `
		UPDATE personal_debts SET project_id = NULL, updated_by_user_id = $1
		WHERE user_id = $1 AND source_transaction_id = $2 AND project_id IS NOT NULL`,
		callerUserID, txID); err != nil {
		return nil, fmt.Errorf("untag debts of %s: %w", txID, err)
	}

	if sourcePT != nil && projectID != nil {
		origin, err := isBoardOriginTx(ctx, tx, callerUserID, *sourcePT)
		if err != nil {
			return nil, err
		}
		if origin {
			children, err := boardSplitsTx(ctx, tx, *sourcePT)
			if err != nil {
				return nil, err
			}
			if err := s.deleteBoardRowTx(ctx, tx, callerUserID, *projectID, *sourcePT); err != nil {
				return nil, err
			}
			if restore {
				if err := s.restoreSplitsTx(ctx, tx, callerUserID, txID, children); err != nil {
					return nil, err
				}
			}
			return children, nil
		}
	}
	_, err := tx.Exec(ctx, `
		UPDATE transactions
		SET project_id = NULL, source_project_transaction_id = NULL, updated_by_user_id = $1
		WHERE id = $2`, callerUserID, txID)
	if err != nil {
		return nil, fmt.Errorf("unlink bill %s: %w", txID, err)
	}
	return nil, nil
}

// boardSplitsTx — board row ptID's member splits, as the people they are:
// a linked member by user, an ad-hoc one by name.
func boardSplitsTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID) ([]quickDebt, error) {
	rows, err := tx.Query(ctx, `
		SELECT pm.user_id, pm.display_name, c.amount
		FROM project_transactions c
		JOIN project_members pm ON pm.id = c.transaction_member_id
		WHERE c.parent_project_transaction_id = $1
		ORDER BY c.created_at, c.id`, ptID)
	if err != nil {
		return nil, fmt.Errorf("board splits of %s: %w", ptID, err)
	}
	defer rows.Close()
	out := make([]quickDebt, 0)
	for rows.Next() {
		var d quickDebt
		if err := rows.Scan(&d.LinkedUserID, &d.PersonName, &d.Amount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// restoreSplitsTx puts a board row's member splits back on the caller's
// txID as personal splits: a linked member through my contact linked to
// them when I have one, else by the member's name. The usual split rules
// follow (a linked contact hears split_created).
func (s *Service) restoreSplitsTx(
	ctx context.Context, tx pgx.Tx, callerUserID, txID uuid.UUID, splits []quickDebt,
) error {
	if len(splits) == 0 {
		return nil
	}
	inputs := make([]transactions.SplitInput, 0, len(splits))
	for _, d := range splits {
		in := transactions.SplitInput{PersonName: d.PersonName, OwedAmount: d.Amount}
		if d.LinkedUserID != nil && *d.LinkedUserID != callerUserID {
			var (
				contactID uuid.UUID
				name      string
			)
			err := tx.QueryRow(ctx, `
				SELECT id, display_name FROM contacts
				WHERE user_id = $1 AND linked_user_id = $2 AND status <> 'archived'
				ORDER BY created_at LIMIT 1`, callerUserID, *d.LinkedUserID).Scan(&contactID, &name)
			switch {
			case err == nil:
				in.ContactID, in.PersonName = &contactID, name
			case !errors.Is(err, pgx.ErrNoRows):
				return fmt.Errorf("contact for member: %w", err)
			}
		}
		inputs = append(inputs, in)
	}
	return s.txs.AddSplitsTx(ctx, tx, callerUserID, txID, inputs)
}

// isBoardOriginTx — ptID is a parent row the caller recorded with
// themselves as the actor.
func isBoardOriginTx(ctx context.Context, tx pgx.Tx, callerUserID, ptID uuid.UUID) (bool, error) {
	var origin bool
	err := tx.QueryRow(ctx, `
		SELECT pt.parent_project_transaction_id IS NULL
		   AND pt.record_user_id = $2
		   AND pm.user_id IS NOT DISTINCT FROM $2
		FROM project_transactions pt
		JOIN project_members pm ON pm.id = pt.transaction_member_id
		WHERE pt.id = $1`, ptID, callerUserID).Scan(&origin)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("board row %s: %w", ptID, err)
	}
	return origin, nil
}

// unlinkCopiesTx turns every personal row pointing at board row ptID or its
// split children into a plain loose transaction. Must run before the board
// row is deleted: the FK's SET NULL alone would leave project_id behind and
// break CHECK transactions_project_id_implies_source.
func unlinkCopiesTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE transactions SET project_id = NULL, source_project_transaction_id = NULL
		WHERE source_project_transaction_id IN (
			SELECT id FROM project_transactions
			WHERE id = $1 OR parent_project_transaction_id = $1)`, ptID)
	if err != nil {
		return fmt.Errorf("unlink copies of %s: %w", ptID, err)
	}
	return nil
}

// deleteBoardRowTx unlinks the copies, deletes board row ptID (children
// cascade) and tells its stakeholders (project_tx_changed, kind deleted).
func (s *Service) deleteBoardRowTx(ctx context.Context, tx pgx.Tx, editorUserID, projectID, ptID uuid.UUID) error {
	// Capture recipients BEFORE delete (FK cascade nukes the children).
	recipients, _ := s.store.PTRecipientsForChange(ctx, ptID, editorUserID)

	if err := unlinkCopiesTx(ctx, tx, ptID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`DELETE FROM project_transactions WHERE id = $1 AND project_id = $2`, ptID, projectID)
	if err != nil {
		return fmt.Errorf("delete PT: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPTNotFound
	}

	if s.notifs == nil || len(recipients) == 0 {
		return nil
	}
	editorName := userDisplayOrEmpty(ctx, tx, editorUserID)
	for _, recipient := range recipients {
		if err := s.notifs.DispatchProjectTxChanged(ctx, tx, recipient, editorUserID,
			notifications.ProjectTxChangedPayload{
				ProjectTransactionID: ptID,
				ProjectID:            projectID,
				EditorUserID:         editorUserID,
				EditorDisplayName:    editorName,
				ChangeKind:           "deleted",
			}, false); err != nil {
			return err
		}
	}
	return nil
}
