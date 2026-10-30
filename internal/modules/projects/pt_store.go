package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const ptColumns = `id, project_id, parent_project_transaction_id,
	transaction_member_id, record_user_id,
	type, amount, currency,
	to_char(date, 'YYYY-MM-DD') AS date,
	note, description,
	marks, created_at, updated_at, payback_kind`

func scanPT(row pgx.Row) (*ProjectTransaction, error) { return scanPTAround(row, nil, nil) }

// scanPTAround reads a row of ptColumns with columns before (lead) and after (trail) them.
func scanPTAround(row pgx.Row, lead, trail []any) (*ProjectTransaction, error) {
	var pt ProjectTransaction
	dest := append(append([]any{}, lead...),
		&pt.ID, &pt.ProjectID, &pt.ParentProjectTransactionID,
		&pt.TransactionMemberID, &pt.RecordUserID,
		&pt.Type, &pt.Amount, &pt.Currency,
		&pt.Date, &pt.Note, &pt.Description,
		&pt.Marks, &pt.CreatedAt, &pt.UpdatedAt, &pt.PaybackKind)
	if err := row.Scan(append(dest, trail...)...); err != nil {
		return nil, err
	}
	return &pt, nil
}

func (s *Store) InsertPTTx(
	ctx context.Context, tx pgx.Tx, projectID, recordUserID uuid.UUID,
	req CreateProjectTransactionRequest,
) (*ProjectTransaction, error) {
	if err := validateSplits(req.TransactionMemberID, req.Amount, req.Splits); err != nil {
		return nil, err
	}
	var currency string
	if err := tx.QueryRow(ctx, `SELECT currency FROM users WHERE id = $1`, recordUserID).Scan(&currency); err != nil {
		return nil, fmt.Errorf("currency: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	parent, err := scanPT(tx.QueryRow(ctx, `INSERT INTO project_transactions
		(id, project_id, transaction_member_id, record_user_id, type, amount, currency,
		 date, note, description, linked_transaction_id, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::date, $9, $10, $11, $4, $4)
		RETURNING `+ptColumns,
		id, projectID, req.TransactionMemberID, recordUserID,
		req.Type, req.Amount, currency, req.Date, shared.CleanText(req.Note), shared.CleanText(req.Description),
		req.LinkedTransactionID,
	))
	if err != nil {
		return nil, fmt.Errorf("insert parent PT: %w", err)
	}
	tagIDs := req.TagIDs
	if len(req.TagNames) > 0 {
		byName, err := ensureEventTagsTx(ctx, tx, projectID, recordUserID, req.TagNames)
		if err != nil {
			return nil, err
		}
		tagIDs = append(tagIDs, byName...)
	}
	if err := setRowTagsTx(ctx, tx, projectID, parent.ID, tagIDs); err != nil {
		return nil, err
	}
	if len(req.Splits) > 0 {
		if err := s.assertMembersInProject(ctx, tx, projectID, splitMemberIDs(req.Splits)); err != nil {
			return nil, err
		}
		if err := s.insertSplitChildrenTx(ctx, tx, parent, recordUserID, req.Splits); err != nil {
			return nil, err
		}
	}
	return parent, nil
}

func splitMemberIDs(splits []ProjectSplitInput) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(splits))
	for _, s := range splits {
		out = append(out, s.MemberID)
	}
	return out
}

func validateSplits(parentMemberID uuid.UUID, parentAmount float64, splits []ProjectSplitInput) error {
	if len(splits) == 0 {
		return nil
	}
	var sum float64
	for _, s := range splits {
		if s.MemberID == parentMemberID {
			return ErrSelfSplit
		}
		sum += s.Amount
	}
	if sum-parentAmount > 0.005 {
		return ErrSplitsExceedParent
	}
	return nil
}

// assertMembersInProject — every memberID must exist in project_members for the given projectID.
func (s *Store) assertMembersInProject(
	ctx context.Context, tx pgx.Tx, projectID uuid.UUID, memberIDs []uuid.UUID,
) error {
	if len(memberIDs) == 0 {
		return nil
	}
	var n int
	err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM project_members
		WHERE project_id = $1 AND id = ANY($2::uuid[])`,
		projectID, memberIDs).Scan(&n)
	if err != nil {
		return fmt.Errorf("member check: %w", err)
	}
	if n != len(memberIDs) { // a member listed twice fails too
		return ErrMemberNotInProject
	}
	return nil
}

func (s *Store) insertSplitChildrenTx(
	ctx context.Context, tx pgx.Tx, parent *ProjectTransaction,
	recordUserID uuid.UUID, splits []ProjectSplitInput,
) error {
	q := `INSERT INTO project_transactions
		(id, project_id, parent_project_transaction_id, transaction_member_id,
		 record_user_id, type, amount, currency, date, note, description,
		 created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10, $11, $5, $5)`
	for _, sp := range splits {
		childID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid: %w", err)
		}
		if _, err := tx.Exec(ctx, q,
			childID, parent.ProjectID, parent.ID, sp.MemberID,
			recordUserID, parent.Type, sp.Amount, parent.Currency, parent.Date, parent.Note, parent.Description,
		); err != nil {
			return fmt.Errorf("insert split child: %w", err)
		}
	}
	return nil
}

// replaceSplitChildrenTx makes the bill's split rows the given list IN PLACE (0.4.1-1 B4): a member
// who stays keeps their row (its id, its ticks, and a copy of my share that points at it), with the
// new amount; a member who left loses their row, and a copy pointing at it is unlinked first (the
// bill is remembered, as "ลงบัญชี" unlink does); a new member gets a new row.
func (s *Store) replaceSplitChildrenTx(
	ctx context.Context, tx pgx.Tx, parent *ProjectTransaction, actorUserID uuid.UUID, splits []ProjectSplitInput,
) error {
	want := map[uuid.UUID]float64{}
	for _, sp := range splits {
		want[sp.MemberID] += sp.Amount
	}
	rows, err := tx.Query(ctx, `SELECT id, transaction_member_id FROM project_transactions
		WHERE parent_project_transaction_id = $1 ORDER BY created_at, id FOR UPDATE`, parent.ID)
	if err != nil {
		return fmt.Errorf("split rows: %w", err)
	}
	type child struct{ id, member uuid.UUID }
	var have []child
	for rows.Next() {
		var c child
		if err := rows.Scan(&c.id, &c.member); err != nil {
			rows.Close()
			return err
		}
		have = append(have, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	kept := map[uuid.UUID]bool{}
	for _, c := range have {
		amount, stays := want[c.member]
		if stays && !kept[c.member] {
			kept[c.member] = true
			if _, err := tx.Exec(ctx, `UPDATE project_transactions SET amount = $2, updated_by_user_id = $3
				WHERE id = $1`, c.id, amount, actorUserID); err != nil {
				return fmt.Errorf("update split row: %w", err)
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE transactions
			SET project_id = NULL, source_project_transaction_id = NULL, unlinked_project_transaction_id = $2
			WHERE source_project_transaction_id = $1`, c.id, parent.ID); err != nil {
			return fmt.Errorf("unlink copy of split row: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM project_transactions WHERE id = $1`, c.id); err != nil {
			return fmt.Errorf("delete split row: %w", err)
		}
	}
	var added []ProjectSplitInput
	for _, sp := range splits {
		if !kept[sp.MemberID] {
			kept[sp.MemberID] = true
			added = append(added, ProjectSplitInput{MemberID: sp.MemberID, Amount: want[sp.MemberID]})
		}
	}
	if len(added) == 0 {
		return nil
	}
	return s.insertSplitChildrenTx(ctx, tx, parent, actorUserID, added)
}

// splitRowOf maps a board split id (a people row) to the old split row it stands for (same bill, same
// member); any other id comes back as it is. For the legacy /project-transactions/{id} endpoints.
func (s *Store) splitRowOf(ctx context.Context, id uuid.UUID) uuid.UUID {
	var row uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT c.id FROM split_transaction_people sp
		JOIN project_transactions c ON c.parent_project_transaction_id = sp.split_transaction_id
		 AND c.transaction_member_id = sp.event_member_id
		WHERE sp.id = $1 LIMIT 1`, id).Scan(&row)
	if err != nil {
		return id
	}
	return row
}

func (s *Store) GetPT(ctx context.Context, ptID uuid.UUID) (*ProjectTransaction, error) {
	return getPT(ctx, s.db, ptID, false)
}

func (s *Store) GetPTTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID) (*ProjectTransaction, error) {
	return getPT(ctx, tx, ptID, true)
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func getPT(ctx context.Context, q rowQuerier, ptID uuid.UUID, lock bool) (*ProjectTransaction, error) {
	sql := `SELECT ` + ptColumns + ` FROM project_transactions WHERE id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	pt, err := scanPT(q.QueryRow(ctx, sql, ptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPTNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	rows, err := q.Query(ctx, `SELECT et.name FROM project_transaction_tags ptt
		JOIN event_tags et ON et.id = ptt.event_tag_id
		WHERE ptt.project_transaction_id = $1 ORDER BY LOWER(et.name)`, pt.ID)
	if err != nil {
		return nil, fmt.Errorf("row tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		pt.TagNames = append(pt.TagNames, n)
	}
	return pt, rows.Err()
}

func (s *Store) UpdatePTTx(
	ctx context.Context, tx pgx.Tx, projectID, ptID, actorUserID uuid.UUID,
	req UpdateProjectTransactionRequest,
) (*ProjectTransaction, error) {
	q := `UPDATE project_transactions SET updated_by_user_id = $1`
	args := []any{actorUserID}
	if req.TransactionMemberID != nil {
		if err := s.assertMembersInProject(ctx, tx, projectID, []uuid.UUID{*req.TransactionMemberID}); err != nil {
			return nil, err
		}
		args = append(args, *req.TransactionMemberID)
		q += fmt.Sprintf(", transaction_member_id = $%d", len(args))
	}
	if req.Amount != nil {
		args = append(args, *req.Amount)
		q += fmt.Sprintf(", amount = $%d", len(args))
	}
	if req.Date != nil {
		args = append(args, *req.Date)
		q += fmt.Sprintf(", date = $%d::date", len(args))
	}
	if v, ok := req.DescriptionChange(); ok {
		args = append(args, v)
		q += fmt.Sprintf(", description = $%d", len(args))
	}
	if v, ok := req.NoteChange(); ok {
		args = append(args, v)
		q += fmt.Sprintf(", note = $%d", len(args))
	}
	args = append(args, ptID, projectID)
	q += fmt.Sprintf(" WHERE id = $%d AND project_id = $%d AND parent_project_transaction_id IS NULL RETURNING %s",
		len(args)-1, len(args), ptColumns)
	parent, err := scanPT(tx.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPTNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update PT: %w", err)
	}
	if req.Date != nil {
		if _, err := tx.Exec(ctx, `UPDATE project_transactions SET date = $2::date
			WHERE parent_project_transaction_id = $1`, parent.ID, parent.Date); err != nil {
			return nil, fmt.Errorf("children date: %w", err)
		}
	}
	if req.TagIDs != nil {
		if err := setRowTagsTx(ctx, tx, projectID, parent.ID, *req.TagIDs); err != nil {
			return nil, err
		}
	}
	if req.Splits != nil {
		if err := validateSplits(parent.TransactionMemberID, parent.Amount, *req.Splits); err != nil {
			return nil, err
		}
		if len(*req.Splits) > 0 {
			if err := s.assertMembersInProject(ctx, tx, projectID, splitMemberIDs(*req.Splits)); err != nil {
				return nil, err
			}
		}
		if err := s.replaceSplitChildrenTx(ctx, tx, parent, actorUserID, *req.Splits); err != nil {
			return nil, err
		}
	} else if req.Amount != nil {
		var sum float64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0) FROM project_transactions
			WHERE parent_project_transaction_id = $1`, parent.ID).Scan(&sum); err != nil {
			return nil, err
		}
		if sum-parent.Amount > 0.005 {
			return nil, ErrSplitsExceedParent
		}
	}
	return getPT(ctx, tx, parent.ID, false)
}

func (s *Store) ToggleMark(
	ctx context.Context, projectID, ptID, projectMemberID uuid.UUID, marked bool,
) error {
	q := `UPDATE project_transactions SET marks = array_remove(marks, $1::uuid)
		WHERE id = $2 AND project_id = $3`
	if marked {
		q = `UPDATE project_transactions SET marks = array_append(marks, $1::uuid)
			WHERE id = $2 AND project_id = $3 AND NOT ($1::uuid = ANY(marks))`
	}
	if _, err := s.db.Exec(ctx, q, projectMemberID, ptID, projectID); err != nil {
		return fmt.Errorf("toggle mark: %w", err)
	}
	return nil
}

func (s *Store) PTRecipientsForChange(
	ctx context.Context, ptID, editorUserID uuid.UUID,
) ([]uuid.UUID, error) {
	// The bill's people, and (0.4.0-3) anyone whose transaction is linked to it or to one of its
	// split rows (a copy). 0.4.1-2 X8: only ACTIVE members of the event (a member who left or was
	// removed hears nothing more), and nobody who disconnected their copy of this bill.
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT u FROM (
			SELECT sp.user_id AS u FROM split_transaction_people sp WHERE sp.split_transaction_id = $1
			UNION
			SELECT t.user_id FROM transactions t
			WHERE t.split_transaction_id = $1 AND t.source_project_transaction_id IS NOT NULL
		) x
		WHERE u IS NOT NULL AND u <> $2
		  AND EXISTS (SELECT 1 FROM project_members am JOIN project_transactions b ON b.project_id = am.project_id
		              WHERE b.id = $1 AND am.user_id = x.u AND am.status = 'active')
		  AND NOT EXISTS (SELECT 1 FROM split_transaction_people o WHERE o.split_transaction_id = $1
		                   AND o.user_id = x.u AND o.disconnected_at IS NOT NULL)`,
		ptID, editorUserID)
	if err != nil {
		return nil, fmt.Errorf("recipients: %w", err)
	}
	defer rows.Close()
	out := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) MemberByUserID(ctx context.Context, projectID, userID uuid.UUID) (*ProjectMember, error) {
	q := `SELECT ` + memberColumns + ` FROM project_members
		WHERE project_id = $1 AND user_id = $2`
	m, err := scanMember(s.db.QueryRow(ctx, q, projectID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotMember
	}
	if err != nil {
		return nil, fmt.Errorf("member lookup: %w", err)
	}
	return m, nil
}

func LoadMemberRefs(ctx context.Context, q shared.Querier, callerUserID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]shared.MemberRef, error) {
	return shared.LoadMemberRefs(ctx, q, callerUserID, ids)
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
