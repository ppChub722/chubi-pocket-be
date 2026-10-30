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
	if err := tx.QueryRow(ctx, `SELECT id FROM project_members WHERE scope_id = $1 AND user_id = $2`,
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
	all, err := collectIDs(ctx, tx, `SELECT DISTINCT sp.split_transaction_id FROM split_transaction_people sp
		JOIN project_transactions b ON b.id = sp.split_transaction_id
		WHERE b.scope_id = $1 AND sp.event_member_id = $2
		UNION SELECT id FROM project_transactions WHERE scope_id = $1 AND transaction_member_id = $2`,
		projectID, memberID)
	if err != nil {
		return nil, err
	}
	bills := make([]uuid.UUID, 0, len(all))
	for _, id := range all {
		if len(req.BillIDs) == 0 || slices.Contains(req.BillIDs, id) {
			bills = append(bills, id)
		}
	}
	// the payer's spot: the new person pays; a share they had in it merges into the payer's rest
	if _, err := tx.Exec(ctx, `UPDATE project_transactions SET transaction_member_id = $3, updated_by_user_id = $4
		WHERE id = ANY($1) AND transaction_member_id = $2`, bills, memberID, to, userID); err != nil {
		return nil, fmt.Errorf("assign payer: %w", err)
	}
	if err := movePersonTx(ctx, tx, bills, memberID, to); err != nil {
		return nil, err
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
