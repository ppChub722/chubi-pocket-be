package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
)

// 0.4.1-2 V15 (Q-E9): a member leaving, or removed by the owner. What they booked from the event
// stays in their book, disconnected from it (they can't open the event any more). Removing also
// leaves their spot on each bill as an empty person "( ) ____" (removed_at) for the owner to assign.

// leaveTx sets the member left (removed = by the owner) and disconnects their book; answers what was
// disconnected.
func (s *Service) leaveTx(ctx context.Context, tx pgx.Tx, projectID, memberID, actorUserID uuid.UUID, removed bool) (*ProjectMember, int, int, error) {
	m, err := scanMember(tx.QueryRow(ctx, `UPDATE project_members SET status = 'left', left_at = NOW(),
		removed_at = CASE WHEN $4 THEN NOW() END, updated_by_user_id = $1
		WHERE id = $2 AND project_id = $3 RETURNING `+memberColumns, actorUserID, memberID, projectID, removed))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, 0, ErrMemberNotFound
	}
	if err != nil {
		return nil, 0, 0, fmt.Errorf("leave: %w", err)
	}
	if m.UserID == nil {
		return m, 0, 0, nil
	}
	txs, debts, err := disconnectBookTx(ctx, tx, projectID, *m.UserID)
	if err != nil {
		return nil, 0, 0, err
	}
	if s.notifs != nil {
		if err := s.notifs.SupersedeEventNoticesForTx(ctx, tx, *m.UserID, projectID, []string{
			notifications.TypeProjectTxChanged, notifications.TypeProjectTxRecordedForYou,
		}); err != nil {
			return nil, 0, 0, err
		}
	}
	return m, txs, debts, nil
}

// disconnectBookTx — userID's transactions and debts of the event become plain records of their
// book: no event, no bill link, no bill parts. A bill whose origin was their tx keeps it as
// unlinked_transaction_id.
func disconnectBookTx(ctx context.Context, tx pgx.Tx, projectID, userID uuid.UUID) (int, int, error) {
	txIDs, err := collectIDs(ctx, tx, `SELECT t.id FROM transactions t WHERE t.user_id = $2 AND (t.project_id = $1
		OR t.source_project_transaction_id IN (SELECT x.id FROM project_transactions x WHERE x.scope_id = $1))`,
		projectID, userID)
	if err != nil {
		return 0, 0, fmt.Errorf("member txs: %w", err)
	}
	if len(txIDs) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE project_transactions
			SET unlinked_transaction_id = linked_transaction_id, linked_transaction_id = NULL
			WHERE project_id = $1 AND linked_transaction_id = ANY($2)`, projectID, txIDs); err != nil {
			return 0, 0, fmt.Errorf("unlink origins: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE transactions SET project_id = NULL, source_project_transaction_id = NULL,
			updated_by_user_id = $2 WHERE id = ANY($1)`, txIDs, userID); err != nil {
			return 0, 0, fmt.Errorf("disconnect txs: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE personal_debt_bills SET transaction_id = NULL WHERE transaction_id = ANY($1)`,
			txIDs); err != nil {
			return 0, 0, fmt.Errorf("disconnect part txs: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM personal_debt_bills WHERE user_id = $2
		AND pt_id IN (SELECT x.id FROM project_transactions x WHERE x.scope_id = $1)`, projectID, userID); err != nil {
		return 0, 0, fmt.Errorf("disconnect parts: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE personal_debts SET project_id = NULL, source_project_transaction_id = NULL,
		source_kind = CASE WHEN source_kind = 'event' THEN NULL ELSE source_kind END, updated_by_user_id = $2
		WHERE user_id = $2 AND (project_id = $1
		   OR source_project_transaction_id IN (SELECT x.id FROM project_transactions x WHERE x.scope_id = $1))`,
		projectID, userID)
	if err != nil {
		return 0, 0, fmt.Errorf("disconnect debts: %w", err)
	}
	return len(txIDs), int(tag.RowsAffected()), nil
}
