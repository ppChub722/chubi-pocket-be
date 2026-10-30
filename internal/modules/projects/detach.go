package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

var ErrBillNotInProject = errors.New("transaction is not in this project")

func (s *Service) RemoveBill(ctx context.Context, callerUserID, projectID, txID uuid.UUID) (*EventPatch, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		ownerID  uuid.UUID
		inProj   *uuid.UUID
		sourcePT *uuid.UUID
	)
	err = tx.QueryRow(ctx, `SELECT user_id, project_id, source_project_transaction_id FROM transactions WHERE id = $1 FOR UPDATE`,
		txID).Scan(&ownerID, &inProj, &sourcePT)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && ownerID != callerUserID) {
		return nil, ErrQuickTxNotFound // not-owned = not-found (no info leak)
	}
	if err != nil {
		return nil, fmt.Errorf("load bill %s: %w", txID, err)
	}
	if inProj == nil || *inProj != projectID {
		return nil, ErrBillNotInProject
	}
	var removed *uuid.UUID
	if sourcePT != nil {
		origin, err := isBoardOriginTx(ctx, tx, callerUserID, *sourcePT)
		if err != nil {
			return nil, err
		}
		if origin {
			removed = sourcePT
		}
	}
	if _, err := s.detachBillTx(ctx, tx, callerUserID, txID, true); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, callerUserID, projectID, nil, removed, nil)
}

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
	// V4 / X5: the tx remembers the bill it left (unlinked_project_transaction_id), so the budget
	// counts it as a plain tx and not the bill's share again.
	_, err := tx.Exec(ctx, `
		UPDATE transactions
		SET project_id = NULL, unlinked_project_transaction_id = source_project_transaction_id,
		    source_project_transaction_id = NULL, updated_by_user_id = $1
		WHERE id = $2`, callerUserID, txID)
	if err != nil {
		return nil, fmt.Errorf("unlink bill %s: %w", txID, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debt_bills SET transaction_id = NULL WHERE transaction_id = $1`, txID); err != nil {
		return nil, fmt.Errorf("unlink bill part tx %s: %w", txID, err)
	}
	return nil, nil
}

func boardSplitsTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID) ([]quickDebt, error) {
	rows, err := tx.Query(ctx, `
		SELECT sp.user_id, sp.name, sp.share
		FROM split_transaction_people sp
		JOIN project_transactions b ON b.id = sp.split_transaction_id
		WHERE sp.split_transaction_id = $1 AND sp.share > 0 AND sp.event_member_id IS DISTINCT FROM b.transaction_member_id
		ORDER BY sp.sort, sp.id`, ptID)
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

func unlinkCopiesTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE transactions SET project_id = NULL, source_project_transaction_id = NULL
		WHERE split_transaction_id = $1 AND source_project_transaction_id IS NOT NULL`, ptID)
	if err != nil {
		return fmt.Errorf("unlink copies of %s: %w", ptID, err)
	}
	return nil
}

func (s *Service) deleteBoardRowTx(ctx context.Context, tx pgx.Tx, editorUserID, projectID, ptID uuid.UUID) error {
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
