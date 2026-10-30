package projects

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// moveBill — 0.4.1-2 S17: PUT /event-bills/{pt_id} with move. The bill (same id), its split rows and
// the paybacks made for it move to another event / a new event / a new split bill; its people become
// members there (added when missing), its event tags follow by name (none on a split bill), and every
// copy stays connected (the copies and debts point at the same bill; their event follows). A split
// bill left with no bill goes. The booking then works on the target.
func (b *booking) moveBill(ctx context.Context, current *ProjectTransaction, mv EventBillMove) error {
	targets := 0
	for _, set := range []bool{mv.ProjectID != nil, mv.NewEvent != nil, mv.Split} {
		if set {
			targets++
		}
	}
	if targets != 1 || (mv.ProjectID != nil && *mv.ProjectID == current.ProjectID) || (mv.Split && b.kind == KindSplit) {
		return ErrMoveTarget
	}
	if mv.NewEvent != nil && (strings.TrimSpace(mv.NewEvent.Name) == "" || len([]rune(mv.NewEvent.Name)) > 100) {
		return ErrBookBillInput
	}
	source, sourceKind := current.ProjectID, b.kind
	rows, err := collectIDs(ctx, b.tx, `SELECT id FROM project_transactions WHERE id = $1 OR paid_back_for_id = $1`,
		current.ID)
	if err != nil {
		return err
	}
	tagNames, err := collectNames(ctx, b, `SELECT et.name FROM project_transaction_tags pt JOIN event_tags et ON et.id = pt.event_tag_id
		WHERE pt.project_transaction_id = $1 ORDER BY et.name`, current.ID)
	if err != nil {
		return err
	}
	switch {
	case mv.ProjectID != nil:
		if err := b.open(ctx, *mv.ProjectID, "create_pt"); err != nil {
			return err
		}
		if b.kind != KindEvent {
			return ErrProjectNotFound
		}
	case mv.NewEvent != nil:
		if err := b.newEvent(ctx, *mv.NewEvent); err != nil {
			return err
		}
	default:
		if err := b.newSplit(ctx, EventBillInput{Description: current.Description}, current.ID); err != nil {
			return err
		}
	}
	target := b.project.ID
	// the people: each member of the moved rows → the same person in the target
	// The rows move first (the completed-event lock reads the new project_id), then their people.
	if _, err := b.tx.Exec(ctx, `UPDATE project_transactions SET project_id = event_or_null($2), updated_by_user_id = $3
		WHERE id = ANY($1)`, rows, target, b.me); err != nil {
		return fmt.Errorf("move bill: %w", err)
	}
	if _, err := b.tx.Exec(ctx, `UPDATE split_transaction_people SET marked_at = NULL
		WHERE split_transaction_id = ANY($1) AND marked_at IS NOT NULL`, rows); err != nil {
		return fmt.Errorf("move marks: %w", err)
	}
	members, err := collectIDs(ctx, b.tx, `SELECT transaction_member_id FROM project_transactions
		WHERE id = ANY($1) AND transaction_member_id IS NOT NULL
		UNION SELECT event_member_id FROM split_transaction_people
		WHERE split_transaction_id = ANY($1) AND event_member_id IS NOT NULL`, rows)
	if err != nil {
		return err
	}
	for _, old := range members {
		var (
			user *uuid.UUID
			name string
		)
		if err := b.tx.QueryRow(ctx, `SELECT user_id, display_name FROM project_members WHERE id = $1`, old).Scan(&user, &name); err != nil {
			return fmt.Errorf("moved member: %w", err)
		}
		var to uuid.UUID
		switch {
		case user != nil && *user == b.me:
			to = b.meMem
		case user != nil:
			to, err = b.mb.ensureLinked(ctx, *user, name)
		default:
			to, err = b.mb.ensureAdHoc(ctx, name)
		}
		if err != nil {
			return err
		}
		if _, err := b.tx.Exec(ctx, `UPDATE project_transactions SET transaction_member_id = $3
			WHERE id = ANY($1) AND transaction_member_id = $2`, rows, old, to); err != nil {
			return fmt.Errorf("move member: %w", err)
		}
		if err := movePersonTx(ctx, b.tx, rows, old, to); err != nil {
			return err
		}
		// every user's bill parts of it point at the same person in the target (lumps survive)
		if _, err := b.tx.Exec(ctx, `UPDATE personal_debt_bills SET member_id = $3
			WHERE pt_id = ANY($1) AND member_id = $2`, rows, old, to); err != nil {
			return fmt.Errorf("move bill parts: %w", err)
		}
	}
	if _, err := b.tx.Exec(ctx, `DELETE FROM project_transaction_tags WHERE project_transaction_id = $1`, current.ID); err != nil {
		return fmt.Errorf("move tags: %w", err)
	}
	if b.kind == KindEvent && len(tagNames) > 0 {
		ids, err := ensureEventTagsTx(ctx, b.tx, target, b.me, tagNames)
		if err != nil {
			return err
		}
		if err := setRowTagsTx(ctx, b.tx, target, current.ID, ids); err != nil {
			return err
		}
	}
	// every copy and debt follows the bill to its new event
	if _, err := b.tx.Exec(ctx, `UPDATE transactions SET project_id = event_or_null($2) WHERE source_project_transaction_id = ANY($1)`,
		rows, target); err != nil {
		return fmt.Errorf("move copies: %w", err)
	}
	if _, err := b.tx.Exec(ctx, `UPDATE personal_debts SET project_id = event_or_null($2)
		WHERE source_project_transaction_id = ANY($1) OR id IN (SELECT debt_id FROM personal_debt_bills WHERE pt_id = $3)`,
		rows, target, current.ID); err != nil {
		return fmt.Errorf("move debts: %w", err)
	}
	if sourceKind == KindSplit { // the split bill's own members are not needed any more
		if _, err := b.tx.Exec(ctx, `DELETE FROM project_members WHERE split_transaction_id = $1`, source); err != nil {
			return fmt.Errorf("drop split members: %w", err)
		}
	}
	current.ProjectID = target
	b.moved = true
	return nil
}

// billToSplitTx — a bill (or a payback of its own) leaves its event and becomes a standalone split
// bill, inside the caller's DB transaction (BE-2: deleting an event keeps its bills). Same as a move
// into a split bill, owned by editor; no notices, no fan-out. Answers the new split's id.
func (s *Service) billToSplitTx(ctx context.Context, tx pgx.Tx, editor, ptID uuid.UUID) (uuid.UUID, error) {
	current, err := s.store.GetPTTx(ctx, tx, ptID)
	if err != nil {
		return uuid.Nil, err
	}
	b := &booking{s: s, tx: tx, me: editor, keys: map[string]shared.PersonRef{}, effects: shared.NewEffects(),
		beforeKey: func(string) error { return nil }, beforeWallet: func(uuid.UUID) error { return nil }}
	if err := tx.QueryRow(ctx, `SELECT kind FROM scopes WHERE id = $1`, current.ProjectID).Scan(&b.kind); err != nil {
		return uuid.Nil, fmt.Errorf("bill event: %w", err)
	}
	if b.kind == KindSplit {
		return current.ProjectID, nil
	}
	if err := b.moveBill(ctx, current, EventBillMove{Split: true}); err != nil {
		return uuid.Nil, err
	}
	return b.project.ID, nil
}

func collectNames(ctx context.Context, b *booking, q string, args ...any) ([]string, error) {
	rows, err := b.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("names: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
