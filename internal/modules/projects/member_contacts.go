package projects

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
)

// myContactJoin — `mc` (id, display_name): the viewer's contact for member row `pm` (0.4.2-3 F3, per
// viewer). In order: a linked (or invited) person → my contact linked to them; my own binding
// (project_member_contacts); a member from before F3 (name_match) → my contact with the same name.
// viewer is a SQL parameter ("$2"); the caller adds the ON clause.
func myContactJoin(viewer string) string {
	return `LEFT JOIN LATERAL (
		SELECT c.id, c.display_name FROM contacts c
		LEFT JOIN project_member_contacts pmc ON pmc.project_member_id = pm.id AND pmc.user_id = ` + viewer + `
		WHERE c.user_id = ` + viewer + ` AND (
		      c.linked_user_id = COALESCE(pm.user_id, pm.invited_user_id)
		   OR c.id = pmc.contact_id
		   OR (pm.name_match AND pm.user_id IS NULL AND pmc.contact_id IS NULL
		       AND LOWER(BTRIM(c.display_name)) = LOWER(BTRIM(pm.display_name))))
		ORDER BY COALESCE(c.linked_user_id = COALESCE(pm.user_id, pm.invited_user_id), FALSE) DESC,
		         COALESCE(c.id = pmc.contact_id, FALSE) DESC,
		         (c.status = 'active') DESC, c.created_at, c.id
		LIMIT 1) mc`
}

// bindContactTx — member memberID is my contact contactID (upsert: one per member and viewer).
func bindContactTx(ctx context.Context, tx pgx.Tx, memberID, userID, contactID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `INSERT INTO project_member_contacts (project_member_id, user_id, contact_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_member_id, user_id) DO UPDATE SET contact_id = EXCLUDED.contact_id`,
		memberID, userID, contactID); err != nil {
		return fmt.Errorf("bind my contact: %w", err)
	}
	return nil
}

// seedMyContactsTx — the builder's contact → member map for the adder: their bindings, then (members
// from before F3 only) their contacts with the same name. Never by name for a newer member.
func (b *quickMemberBuilder) seedMyContactsTx(ctx context.Context) error {
	rows, err := b.tx.Query(ctx, `
		SELECT pmc.contact_id, pm.id, 0 FROM project_member_contacts pmc
		JOIN project_members pm ON pm.id = pmc.project_member_id
		WHERE pm.project_id = $1 AND pmc.user_id = $2 AND pm.status <> 'left'
		UNION ALL
		SELECT c.id, pm.id, 1 FROM project_members pm
		JOIN contacts c ON c.user_id = $2 AND c.linked_user_id IS NULL
		 AND LOWER(BTRIM(c.display_name)) = LOWER(BTRIM(pm.display_name))
		WHERE pm.project_id = $1 AND pm.name_match AND pm.user_id IS NULL AND pm.status <> 'left'
		ORDER BY 3`, b.projectID, b.callerUserID)
	if err != nil {
		return fmt.Errorf("my contacts in the event: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			contactID, memberID uuid.UUID
			rank                int
		)
		if err := rows.Scan(&contactID, &memberID, &rank); err != nil {
			return err
		}
		if _, ok := b.byContact[contactID]; !ok {
			b.byContact[contactID] = memberID
		}
	}
	return rows.Err()
}

var ErrMemberIsMe = apierr.Invalid("that member is you", "member_id", apierr.FieldInvalid)

// BindMyContactRequest — PUT /projects/:id/members/:member_id/my-contact.
type BindMyContactRequest struct {
	ContactID uuid.UUID `json:"contact_id" binding:"required"`
}

// BindMyContact — "ผูกกับผู้ติดต่อของฉัน": the member is my contact, for me only (F3).
func (s *Service) BindMyContact(ctx context.Context, callerUserID, projectID, memberID uuid.UUID, contactID *uuid.UUID) (*EventPatch, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	me, err := s.store.MemberByUserIDTx(ctx, tx, projectID, callerUserID)
	if err != nil {
		return nil, err
	}
	if me.Status == MemberStatusLeft {
		return nil, ErrNotMember
	}
	target, err := s.store.GetMemberTx(ctx, tx, memberID)
	if err != nil {
		return nil, err
	}
	if target.ProjectID != projectID {
		return nil, ErrMemberNotInProject
	}
	if target.ID == me.ID {
		return nil, ErrMemberIsMe
	}
	if contactID == nil {
		if _, err := tx.Exec(ctx, `DELETE FROM project_member_contacts WHERE project_member_id = $1 AND user_id = $2`,
			memberID, callerUserID); err != nil {
			return nil, fmt.Errorf("unbind my contact: %w", err)
		}
	} else {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM contacts WHERE id = $1 AND user_id = $2)`,
			*contactID, callerUserID).Scan(&ok); err != nil {
			return nil, fmt.Errorf("my contact: %w", err)
		}
		if !ok {
			return nil, transactions.ErrSplitContactNotFound
		}
		if err := bindContactTx(ctx, tx, memberID, callerUserID, *contactID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, callerUserID, projectID, nil, nil, nil)
}
