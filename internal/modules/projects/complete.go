package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// EventCompleteResult — POST /projects/{id}/complete.
type EventCompleteResult struct {
	EventPatch
	Effects shared.Effects `json:"effects"`
}

// Complete — 0.4.1-2 V2: the owner locks an active event for good, in one DB transaction. Every
// book stops syncing (the links stay, frozen); pending notices about its bills are superseded; the
// other linked members hear about it.
func (s *Service) Complete(ctx context.Context, userID, id uuid.UUID) (*EventCompleteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	var (
		owner  uuid.UUID
		status string
		name   string
	)
	err = tx.QueryRow(ctx, `SELECT p.owner_user_id, p.status, p.name FROM projects p
		WHERE p.id = $1 AND EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $2)
		FOR UPDATE OF p`, id, userID).Scan(&owner, &status, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock event: %w", err)
	}
	if owner != userID {
		return nil, ErrNotOwner
	}
	if status != StatusActive {
		return nil, ErrCompleteNotActive
	}
	empty, err := collectIDs(ctx, tx, `SELECT DISTINCT COALESCE(pt.parent_project_transaction_id, pt.id)
		FROM project_transactions pt JOIN project_members pm ON pm.id = pt.transaction_member_id
		WHERE pt.project_id = $1 AND pm.removed_at IS NOT NULL`, id)
	if err != nil {
		return nil, fmt.Errorf("empty persons: %w", err)
	}
	if len(empty) > 0 {
		return nil, &EmptyPersonError{BillIDs: empty}
	}
	if _, err := tx.Exec(ctx, `UPDATE projects SET status = 'completed', completed_at = NOW(), updated_by_user_id = $2
		WHERE id = $1`, id, userID); err != nil {
		return nil, fmt.Errorf("complete: %w", err)
	}
	myTxs, err := collectIDs(ctx, tx, `SELECT t.id FROM transactions t WHERE t.user_id = $2 AND (t.project_id = $1
		OR t.source_project_transaction_id IN (SELECT x.id FROM project_transactions x WHERE x.project_id = $1))`, id, userID)
	if err != nil {
		return nil, fmt.Errorf("my txs: %w", err)
	}
	myDebts, err := collectIDs(ctx, tx, `SELECT d.id FROM personal_debts d WHERE d.user_id = $2 AND (d.project_id = $1
		OR d.id IN (SELECT b.debt_id FROM personal_debt_bills b JOIN project_transactions x ON x.id = b.pt_id
			WHERE b.user_id = $2 AND x.project_id = $1))`, id, userID)
	if err != nil {
		return nil, fmt.Errorf("my debts: %w", err)
	}
	if s.notifs != nil {
		if err := s.notifs.SupersedeEventNoticesTx(ctx, tx, id, []string{
			notifications.TypeProjectTxChanged, notifications.TypeProjectTxRecordedForYou,
		}); err != nil {
			return nil, err
		}
		others, err := collectIDs(ctx, tx, `SELECT user_id FROM project_members
			WHERE project_id = $1 AND user_id IS NOT NULL AND user_id <> $2 AND status = 'active'`, id, userID)
		if err != nil {
			return nil, fmt.Errorf("members: %w", err)
		}
		p := notifications.EventCompletedPayload{ProjectID: id, ProjectName: name, OwnerName: userDisplayOrEmpty(ctx, tx, userID)}
		for _, u := range others {
			if err := s.notifs.DispatchEventCompleted(ctx, tx, u, userID, p); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	patch, err := s.patch(ctx, userID, id, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	out := &EventCompleteResult{EventPatch: *patch, Effects: shared.NewEffects()}
	for _, tid := range myTxs {
		if d, err := s.txs.Get(ctx, userID, tid); err == nil {
			out.Effects.Transactions = append(out.Effects.Transactions, *d)
		}
	}
	if s.debts != nil && len(myDebts) > 0 {
		if out.Effects.Debts, _, err = s.debts.DebtEffects(ctx, userID, myDebts, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}
