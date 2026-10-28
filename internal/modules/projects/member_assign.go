package projects

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrMemberNotRemoved — only a removed member's spots are assigned.
var ErrMemberNotRemoved = errors.New("only a removed member's spots can be assigned")

// AssignRemovedRequest — POST /projects/{id}/members/{member_id}/assign (0.4.1-2 V15).
type AssignRemovedRequest struct {
	To      BillPerson  `json:"to"`
	BillIDs []uuid.UUID `json:"bill_ids"` // empty = every bill of that member
}

// AssignRemoved gives a removed member's spots (payer or share) to another person: a member, me, a
// contact or a typed name (added to the event when not a member yet). A share that meets the new
// person's own share in the same bill is added to it; a share that meets the payer goes back to the
// payer's rest. One DB transaction; answers the patch.
func (s *Service) AssignRemoved(ctx context.Context, userID, projectID, memberID uuid.UUID, req AssignRemovedRequest) (*EventPatch, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := s.store.AssertWritable(ctx, projectID, "edit_pt"); err != nil {
		return nil, err
	}
	p, _, mb, err := s.billWriterTx(ctx, tx, userID, projectID)
	if err != nil {
		return nil, err
	}
	var removed bool
	err = tx.QueryRow(ctx, `SELECT removed_at IS NOT NULL FROM project_members WHERE id = $1 AND project_id = $2`,
		memberID, projectID).Scan(&removed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("removed member: %w", err)
	}
	if !removed {
		return nil, ErrMemberNotRemoved
	}
	var meID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID).Scan(&meID); err != nil {
		return nil, fmt.Errorf("my member: %w", err)
	}
	to, err := mb.memberForPerson(ctx, req.To, meID)
	if err != nil {
		return nil, err
	}
	if to == memberID {
		return nil, ErrBadBillPeople
	}
	type spot struct {
		id     uuid.UUID
		parent *uuid.UUID
		amount float64
	}
	rows, err := tx.Query(ctx, `SELECT id, parent_project_transaction_id, amount::float8 FROM project_transactions
		WHERE project_id = $1 AND transaction_member_id = $2 ORDER BY parent_project_transaction_id NULLS FIRST, id`,
		projectID, memberID)
	if err != nil {
		return nil, fmt.Errorf("spots: %w", err)
	}
	var spots []spot
	for rows.Next() {
		var sp spot
		if err := rows.Scan(&sp.id, &sp.parent, &sp.amount); err != nil {
			rows.Close()
			return nil, err
		}
		bill := sp.id
		if sp.parent != nil {
			bill = *sp.parent
		}
		if len(req.BillIDs) == 0 || slices.Contains(req.BillIDs, bill) {
			spots = append(spots, sp)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, sp := range spots {
		if sp.parent == nil { // the payer: the new person pays; their own share in it merges into the rest
			if _, err := tx.Exec(ctx, `DELETE FROM project_transactions WHERE parent_project_transaction_id = $1
				AND transaction_member_id = $2`, sp.id, to); err != nil {
				return nil, fmt.Errorf("assign payer share: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE project_transactions SET transaction_member_id = $2, updated_by_user_id = $3
				WHERE id = $1`, sp.id, to, userID); err != nil {
				return nil, fmt.Errorf("assign payer: %w", err)
			}
			continue
		}
		var payer uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT transaction_member_id FROM project_transactions WHERE id = $1`, *sp.parent).
			Scan(&payer); err != nil {
			return nil, fmt.Errorf("bill payer: %w", err)
		}
		tag, err := tx.Exec(ctx, `UPDATE project_transactions SET amount = amount + $3, updated_by_user_id = $4
			WHERE parent_project_transaction_id = $1 AND transaction_member_id = $2`, *sp.parent, to, sp.amount, userID)
		if err != nil {
			return nil, fmt.Errorf("assign share: %w", err)
		}
		if tag.RowsAffected() > 0 || payer == to { // merged into their share / the payer's rest
			if _, err := tx.Exec(ctx, `DELETE FROM project_transactions WHERE id = $1`, sp.id); err != nil {
				return nil, fmt.Errorf("drop spot: %w", err)
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE project_transactions SET transaction_member_id = $2, updated_by_user_id = $3
			WHERE id = $1`, sp.id, to, userID); err != nil {
			return nil, fmt.Errorf("assign share: %w", err)
		}
	}
	name := userDisplayOrEmpty(ctx, tx, userID)
	if err := s.dispatchAddedTx(ctx, tx, p, mb, name); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, userID, projectID, nil, nil, nil)
}
