package projects

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
)

// deleteEventBillTx — 0.4.1-2 V13 (fixes X6): an EVENT bill is deleted. Each member's records of it
// (their copy transaction, their debts / lump parts of it, the "คืนแล้ว" repayments of the paybacks
// made for it) follow THAT member's auto_sync_events:
//   - on → they are deleted (a lump shrinks; a debt of this bill only goes), the notice is info;
//   - off → they stay as plain records (no event, no bill link) and shared_bill_deleted asks
//     (applied false; apply = remove them).
//
// The editor's own records go (keepMine = they stay plain, no notice). The paybacks made for the bill
// go with it. Answers what the editor's side touched. The caller checked the rights and locked pt.
func (s *Service) deleteEventBillTx(ctx context.Context, tx pgx.Tx, editor uuid.UUID, pt *ProjectTransaction,
	keepMine bool) (*copyResult, error) {
	st, err := s.loadBillTx(ctx, tx, pt.ID)
	if err != nil {
		return nil, err
	}
	users, err := collectIDs(ctx, tx, `SELECT DISTINCT pm.user_id FROM project_members pm
		WHERE pm.project_id = $1 AND pm.user_id IS NOT NULL AND pm.status = 'active'
		  AND NOT EXISTS (SELECT 1 FROM split_transaction_people o WHERE o.split_transaction_id = $2
		                   AND o.user_id = pm.user_id AND o.disconnected_at IS NOT NULL)`,
		pt.ProjectID, pt.ID)
	if err != nil {
		return nil, err
	}
	mine := &copyResult{}
	for _, u := range users {
		copyID, err := myLinkedTxTx(ctx, tx, u, pt.ID)
		if err != nil {
			return nil, err
		}
		sync := !keepMine
		if u != editor {
			d, err := s.notifs.DeliveryFor(ctx, tx, u, &editor, notifications.TypeSharedBillDeleted)
			if err != nil {
				return nil, err
			}
			sync = d.SyncEvents
		}
		out := &copyResult{}
		if u == editor {
			out = mine
		}
		var txIDs, debtIDs []uuid.UUID
		if sync {
			noop := &copyOptions{beforeKey: func(string) error { return nil }, beforeWallet: func(uuid.UUID) error { return nil }}
			if err := s.dropCopyTx(ctx, tx, st, u, copyID, noop, out); err != nil {
				return nil, err
			}
			txIDs, debtIDs = out.removedTxs, out.removedDebts
		} else if txIDs, debtIDs, err = plainBillRecordsTx(ctx, tx, u, pt.ID, copyID); err != nil {
			return nil, err
		}
		if u == editor || (len(txIDs) == 0 && len(debtIDs) == 0) {
			continue
		}
		name := st.name
		if err := s.notifs.SupersedeBillNoticesTx(ctx, tx, u, pt.ID); err != nil {
			return nil, err
		}
		if err := s.notifs.DispatchSharedBillDeleted(ctx, tx, u, editor, notifications.SharedBillDeletedPayload{
			ProjectTransactionID: pt.ID, ProjectID: &pt.ProjectID, ProjectKind: KindEvent, ProjectName: &name,
			Description: pt.Description, Amount: pt.Amount, MyTransactionIDs: nonNilIDs(txIDs), MyDebtIDs: nonNilIDs(debtIDs),
			Applied: sync,
		}); err != nil {
			return nil, err
		}
	}
	// the bill and the paybacks made for it ("คืนแล้ว" ticks)
	if _, err := tx.Exec(ctx, `DELETE FROM project_transactions WHERE paid_back_for_id = $1`, pt.ID); err != nil {
		return nil, fmt.Errorf("delete paybacks of bill: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_transactions WHERE id = $1`, pt.ID); err != nil {
		return nil, fmt.Errorf("delete bill: %w", err)
	}
	return mine, nil
}

// plainBillRecordsTx — the "off" side of a deleted bill: the user's records of it stay, plain. Their
// bill parts go without changing a debt's amount (a debt left with no part loses its event source).
// Answers the records kept (for the notice, so "apply" can remove them).
func plainBillRecordsTx(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID, copyID *uuid.UUID) ([]uuid.UUID, []uuid.UUID, error) {
	var txIDs []uuid.UUID
	if copyID != nil {
		txIDs = append(txIDs, *copyID)
		if _, err := tx.Exec(ctx, `UPDATE project_transactions SET linked_transaction_id = NULL WHERE linked_transaction_id = $1`,
			*copyID); err != nil {
			return nil, nil, fmt.Errorf("unlink origin: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE transactions SET project_id = NULL, source_project_transaction_id = NULL,
			updated_by_user_id = $2 WHERE id = $1`, *copyID, userID); err != nil {
			return nil, nil, fmt.Errorf("plain copy: %w", err)
		}
	}
	debtIDs, err := collectIDs(ctx, tx, `SELECT DISTINCT debt_id FROM personal_debt_bills WHERE user_id = $1 AND pt_id = $2`,
		userID, ptID)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM personal_debt_bills WHERE user_id = $1 AND pt_id = $2`, userID, ptID); err != nil {
		return nil, nil, fmt.Errorf("drop parts: %w", err)
	}
	repays, err := collectIDs(ctx, tx, `SELECT id FROM personal_debts WHERE user_id = $1 AND (source_project_transaction_id IN (
			SELECT id FROM project_transactions WHERE id = $2 OR parent_project_transaction_id = $2 OR paid_back_for_id = $2)
		OR id = ANY($3))`, userID, ptID, debtIDs)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debts SET source_project_transaction_id = NULL, project_id = NULL,
		source_kind = CASE WHEN source_kind = 'event' THEN NULL ELSE source_kind END, updated_by_user_id = $1
		WHERE id = ANY($2) AND NOT EXISTS (SELECT 1 FROM personal_debt_bills b WHERE b.debt_id = personal_debts.id)`,
		userID, repays); err != nil {
		return nil, nil, fmt.Errorf("plain debts: %w", err)
	}
	return txIDs, repays, nil
}

func nonNilIDs(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
