package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const ptColumns = `id, scope_id AS project_id, parent_project_transaction_id,
	transaction_member_id, record_user_id,
	type, amount, currency,
	to_char(date, 'YYYY-MM-DD') AS date,
	note, description,
	'{}'::uuid[] AS marks, created_at, updated_at, payback_kind`

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
	id := req.ID
	if id == uuid.Nil {
		var err error
		if id, err = uuid.NewV7(); err != nil {
			return nil, fmt.Errorf("uuid: %w", err)
		}
	}
	parent, err := scanPT(tx.QueryRow(ctx, `INSERT INTO project_transactions
		(id, project_id, transaction_member_id, record_user_id, type, amount, currency,
		 date, note, description, linked_transaction_id, created_by_user_id, updated_by_user_id)
		VALUES ($1, event_or_null($2), $3, $4, $5, $6, $7, $8::date, $9, $10, $11, $4, $4)
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
	}
	if err := putBillPeopleTx(ctx, tx, parent.ID, billPeopleWrite{Splits: splitShares(req.Splits)}); err != nil {
		return nil, err
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

// assertMembersInProject — every memberID must be a member of scope (an event, or a split bill).
func (s *Store) assertMembersInProject(
	ctx context.Context, tx pgx.Tx, scope uuid.UUID, memberIDs []uuid.UUID,
) error {
	if len(memberIDs) == 0 {
		return nil
	}
	var n int
	err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM project_members
		WHERE scope_id = $1 AND id = ANY($2::uuid[])`,
		scope, memberIDs).Scan(&n)
	if err != nil {
		return fmt.Errorf("member check: %w", err)
	}
	if n != len(memberIDs) { // a member listed twice fails too
		return ErrMemberNotInProject
	}
	return nil
}

// splitShares — the split list as shares by member (a member listed twice adds up).
func splitShares(splits []ProjectSplitInput) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	for _, sp := range splits {
		out[sp.MemberID] += sp.Amount
	}
	return out
}

// billOfPerson maps a board split id (a people row) to its bill and member; ok = false when the id
// is not a person (a bill id). For the legacy /project-transactions/{id} endpoints.
func (s *Store) billOfPerson(ctx context.Context, id uuid.UUID) (bill, member uuid.UUID, ok bool) {
	err := s.db.QueryRow(ctx, `SELECT split_transaction_id, event_member_id FROM split_transaction_people
		WHERE id = $1 AND event_member_id IS NOT NULL`, id).Scan(&bill, &member)
	return bill, member, err == nil
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
	q += fmt.Sprintf(" WHERE id = $%d AND scope_id = $%d AND parent_project_transaction_id IS NULL RETURNING %s",
		len(args)-1, len(args), ptColumns)
	parent, err := scanPT(tx.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPTNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update PT: %w", err)
	}
	if req.TagIDs != nil {
		if err := setRowTagsTx(ctx, tx, projectID, parent.ID, *req.TagIDs); err != nil {
			return nil, err
		}
	}
	var w billPeopleWrite
	if req.Splits != nil {
		if err := validateSplits(parent.TransactionMemberID, parent.Amount, *req.Splits); err != nil {
			return nil, err
		}
		if len(*req.Splits) > 0 {
			if err := s.assertMembersInProject(ctx, tx, projectID, splitMemberIDs(*req.Splits)); err != nil {
				return nil, err
			}
		}
		w.Splits = splitShares(*req.Splits)
	} else if req.Amount != nil {
		var sum float64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(share), 0) FROM split_transaction_people
			WHERE split_transaction_id = $1 AND sort > 0`, parent.ID).Scan(&sum); err != nil {
			return nil, err
		}
		if sum-parent.Amount > 0.005 {
			return nil, ErrSplitsExceedParent
		}
	}
	if req.Splits != nil || req.Amount != nil || req.TransactionMemberID != nil {
		if err := putBillPeopleTx(ctx, tx, parent.ID, w); err != nil {
			return nil, err
		}
	}
	return getPT(ctx, tx, parent.ID, false)
}

func (s *Store) ToggleMark(
	ctx context.Context, projectID, ptID, projectMemberID uuid.UUID, marked bool,
) error {
	q := `UPDATE split_transaction_people sp SET marked_at = NULL
		FROM project_transactions b WHERE sp.split_transaction_id = b.id
		  AND sp.event_member_id = $1 AND b.id = $2 AND b.scope_id = $3 AND sp.marked_at IS NOT NULL`
	if marked {
		q = `UPDATE split_transaction_people sp SET marked_at = NOW()
			FROM project_transactions b WHERE sp.split_transaction_id = b.id
			  AND sp.event_member_id = $1 AND b.id = $2 AND b.scope_id = $3 AND sp.marked_at IS NULL`
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
		  AND EXISTS (SELECT 1 FROM project_members am JOIN project_transactions b
		                ON am.scope_id = b.scope_id
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
		WHERE scope_id = $1 AND user_id = $2`
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
