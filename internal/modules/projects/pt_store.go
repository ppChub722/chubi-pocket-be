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
	marks, created_at, updated_at`

func scanPT(row pgx.Row) (*ProjectTransaction, error) {
	var pt ProjectTransaction
	err := row.Scan(
		&pt.ID, &pt.ProjectID, &pt.ParentProjectTransactionID,
		&pt.TransactionMemberID, &pt.RecordUserID,
		&pt.Type, &pt.Amount, &pt.Currency,
		&pt.Date, &pt.Note, &pt.Description,
		&pt.Marks, &pt.CreatedAt, &pt.UpdatedAt,
	)
	if err != nil {
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
		if _, err := tx.Exec(ctx,
			`DELETE FROM project_transactions WHERE parent_project_transaction_id = $1`,
			parent.ID); err != nil {
			return nil, fmt.Errorf("delete old children: %w", err)
		}
		if len(*req.Splits) > 0 {
			if err := s.assertMembersInProject(ctx, tx, projectID, splitMemberIDs(*req.Splits)); err != nil {
				return nil, err
			}
			if err := s.insertSplitChildrenTx(ctx, tx, parent, actorUserID, *req.Splits); err != nil {
				return nil, err
			}
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
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT pm.user_id FROM (
			SELECT transaction_member_id AS member_id FROM project_transactions WHERE id = $1
			UNION
			SELECT transaction_member_id FROM project_transactions WHERE parent_project_transaction_id = $1
		) sub
		JOIN project_members pm ON pm.id = sub.member_id
		WHERE pm.user_id IS NOT NULL AND pm.user_id <> $2`, ptID, editorUserID)
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
	out := map[uuid.UUID]shared.MemberRef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT pm.id, pm.user_id,
			COALESCE(u.display_name, pm.display_name), COALESCE(u.icon_code, pm.icon_code)
		FROM project_members pm LEFT JOIN users u ON u.id = pm.user_id
		WHERE pm.id = ANY($1::uuid[])`, uniqueIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("member refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r    shared.MemberRef
			icon []byte
		)
		if err := rows.Scan(&r.ID, &r.UserID, &r.Name, &icon); err != nil {
			return nil, err
		}
		r.Icon = shared.IconFromJSON(icon)
		r.IsMe = r.UserID != nil && *r.UserID == callerUserID
		out[r.ID] = r
	}
	return out, rows.Err()
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
