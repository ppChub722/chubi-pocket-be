package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const ptColumns = `id, project_id, parent_project_transaction_id,
	transaction_member_id, record_user_id,
	type, amount, currency,
	to_char(date, 'YYYY-MM-DD') AS date,
	note, description, category_name, category_icon_id, category_color_id,
	marks, created_at, updated_at`

func scanPT(row pgx.Row) (*ProjectTransaction, error) {
	var pt ProjectTransaction
	err := row.Scan(
		&pt.ID, &pt.ProjectID, &pt.ParentProjectTransactionID,
		&pt.TransactionMemberID, &pt.RecordUserID,
		&pt.Type, &pt.Amount, &pt.Currency,
		&pt.Date, &pt.Note, &pt.Description,
		&pt.CategoryName, &pt.CategoryIconID, &pt.CategoryColorID,
		&pt.Marks, &pt.CreatedAt, &pt.UpdatedAt,
	)
	return &pt, err
}

// InsertPTTx inserts a parent project_transaction row plus any child split
// rows atomically. Children inherit type/currency/date/note from the parent;
// the caller does not send those fields per child.
//
// Validation enforced here:
//   - each split.member_id belongs to the same project
//   - no self-split (split.member_id != parent.transaction_member_id)
//   - sum(splits.amount) <= parent.amount
func (s *Store) InsertPTTx(
	ctx context.Context, tx pgx.Tx, projectID, recordUserID uuid.UUID,
	req CreateProjectTransactionRequest,
) (*ProjectTransaction, error) {
	if err := validateSplits(req.TransactionMemberID, req.Amount, req.Splits); err != nil {
		return nil, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	q := `INSERT INTO project_transactions
		(id, project_id, transaction_member_id, record_user_id, type, amount, currency,
		 date, note, description, category_name, category_icon_id, category_color_id,
		 created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::date, $9, $10, $11, $12, $13, $4, $4)
		RETURNING ` + ptColumns
	parent, err := scanPT(tx.QueryRow(ctx, q,
		id, projectID, req.TransactionMemberID, recordUserID,
		req.Type, req.Amount, req.Currency, req.Date, req.Note, req.Description,
		req.CategoryName, req.CategoryIconID, req.CategoryColorID,
	))
	if err != nil {
		return nil, fmt.Errorf("insert parent PT: %w", err)
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
	// Allow tiny floating-point slack (1 cent).
	if sum-parentAmount > 0.005 {
		return ErrSplitsExceedParent
	}
	return nil
}

// assertMembersInProject — every memberID must row-exist in project_members
// for the given projectID.
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
	if n != len(memberIDs) {
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
		 category_name, category_icon_id, category_color_id,
		 created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10, $11, $12, $13, $14, $5, $5)`
	for _, sp := range splits {
		childID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid: %w", err)
		}
		if _, err := tx.Exec(ctx, q,
			childID, parent.ProjectID, parent.ID, sp.MemberID,
			recordUserID, parent.Type, sp.Amount, parent.Currency, parent.Date, parent.Note, parent.Description,
			parent.CategoryName, parent.CategoryIconID, parent.CategoryColorID,
		); err != nil {
			return fmt.Errorf("insert split child: %w", err)
		}
	}
	return nil
}

func (s *Store) GetPT(ctx context.Context, ptID uuid.UUID) (*ProjectTransaction, error) {
	q := `SELECT ` + ptColumns + ` FROM project_transactions WHERE id = $1`
	pt, err := scanPT(s.db.QueryRow(ctx, q, ptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPTNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return pt, nil
}

// GetPTTx locks the row for update.
func (s *Store) GetPTTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID) (*ProjectTransaction, error) {
	q := `SELECT ` + ptColumns + ` FROM project_transactions WHERE id = $1 FOR UPDATE`
	pt, err := scanPT(tx.QueryRow(ctx, q, ptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPTNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return pt, nil
}

// UpdatePTTx applies scalar field updates (amount/date/note) and, when
// req.Splits is non-nil, full-replaces the children. Returns the refreshed
// parent row.
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
	if req.Note != nil {
		args = append(args, *req.Note)
		q += fmt.Sprintf(", note = $%d", len(args))
	}
	if req.Description != nil {
		args = append(args, *req.Description)
		q += fmt.Sprintf(", description = $%d", len(args))
	}
	if req.CategoryName != nil {
		args = append(args, *req.CategoryName)
		q += fmt.Sprintf(", category_name = $%d", len(args))
	}
	if req.CategoryIconID != nil {
		args = append(args, *req.CategoryIconID)
		q += fmt.Sprintf(", category_icon_id = $%d", len(args))
	}
	if req.CategoryColorID != nil {
		args = append(args, *req.CategoryColorID)
		q += fmt.Sprintf(", category_color_id = $%d", len(args))
	}
	args = append(args, ptID)
	q += fmt.Sprintf(" WHERE id = $%d AND project_id = ", len(args))
	args = append(args, projectID)
	q += fmt.Sprintf("$%d AND parent_project_transaction_id IS NULL RETURNING %s",
		len(args), ptColumns)

	parent, err := scanPT(tx.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPTNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update PT: %w", err)
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
	}
	return parent, nil
}

func (s *Store) DeletePT(ctx context.Context, projectID, ptID uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM project_transactions WHERE id = $1 AND project_id = $2`, ptID, projectID)
	if err != nil {
		return fmt.Errorf("delete PT: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPTNotFound
	}
	return nil
}

// ListPT paginates parents only — children of returned parents are appended
// without counting against per_page. Caller (FE) groups parent + children
// into a tree.
func (s *Store) ListPT(ctx context.Context, projectID uuid.UUID, page, perPage int) ([]ProjectTransaction, int, error) {
	var total int
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM project_transactions
		WHERE project_id = $1 AND parent_project_transaction_id IS NULL`,
		projectID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}

	parentsQ := `SELECT ` + ptColumns + ` FROM project_transactions
		WHERE project_id = $1 AND parent_project_transaction_id IS NULL
		ORDER BY date DESC, created_at DESC
		LIMIT $2 OFFSET $3`
	rows, err := s.db.Query(ctx, parentsQ, projectID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("list PT: %w", err)
	}
	defer rows.Close()

	parents := make([]ProjectTransaction, 0, perPage)
	parentIDs := make([]uuid.UUID, 0, perPage)
	for rows.Next() {
		pt, err := scanPT(rows)
		if err != nil {
			return nil, 0, err
		}
		parents = append(parents, *pt)
		parentIDs = append(parentIDs, pt.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	if len(parentIDs) == 0 {
		return parents, total, nil
	}

	childrenQ := `SELECT ` + ptColumns + ` FROM project_transactions
		WHERE parent_project_transaction_id = ANY($1::uuid[])
		ORDER BY created_at ASC`
	childRows, err := s.db.Query(ctx, childrenQ, parentIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("list PT children: %w", err)
	}
	defer childRows.Close()
	for childRows.Next() {
		pt, err := scanPT(childRows)
		if err != nil {
			return nil, 0, err
		}
		parents = append(parents, *pt)
	}
	return parents, total, childRows.Err()
}

// ToggleMark adds (marked=true) or removes (marked=false) projectMemberID
// from the marks array. Idempotent in both directions.
func (s *Store) ToggleMark(
	ctx context.Context, projectID, ptID, projectMemberID uuid.UUID, marked bool,
) (*ProjectTransaction, error) {
	var q string
	if marked {
		q = `UPDATE project_transactions
			SET marks = array_append(marks, $1::uuid)
			WHERE id = $2 AND project_id = $3 AND NOT ($1::uuid = ANY(marks))
			RETURNING ` + ptColumns
	} else {
		q = `UPDATE project_transactions
			SET marks = array_remove(marks, $1::uuid)
			WHERE id = $2 AND project_id = $3
			RETURNING ` + ptColumns
	}
	pt, err := scanPT(s.db.QueryRow(ctx, q, projectMemberID, ptID, projectID))
	if errors.Is(err, pgx.ErrNoRows) {
		// No-change cases: marking when already marked, or unmarking when
		// not in the array — fall back to a plain GetPT so the caller still
		// gets the current row.
		return s.GetPT(ctx, ptID)
	}
	if err != nil {
		return nil, fmt.Errorf("toggle mark: %w", err)
	}
	return pt, nil
}

// PTRecipientsForChange returns the linked users with a stake in the given
// PT (the actor on the parent + all split debtors), MINUS `editorUserID`.
// Used to fan-out project_tx_changed notifications.
func (s *Store) PTRecipientsForChange(
	ctx context.Context, ptID, editorUserID uuid.UUID,
) ([]uuid.UUID, error) {
	q := `
		SELECT DISTINCT pm.user_id FROM (
			SELECT transaction_member_id AS member_id FROM project_transactions
			WHERE id = $1
			UNION
			SELECT transaction_member_id FROM project_transactions
			WHERE parent_project_transaction_id = $1
		) sub
		JOIN project_members pm ON pm.id = sub.member_id
		WHERE pm.user_id IS NOT NULL AND pm.user_id <> $2`
	rows, err := s.db.Query(ctx, q, ptID, editorUserID)
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

// SummaryAggregate returns counts + totals for a project. Children rows are
// excluded from the totals (parents' amounts are authoritative — children
// just split parents).
func (s *Store) SummaryAggregate(ctx context.Context, projectID, callerID uuid.UUID) (*SummaryResponse, error) {
	resp := &SummaryResponse{ProjectID: projectID}
	err := s.db.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(amount) FILTER (WHERE type = 'expense'), 0),
			COALESCE(SUM(amount) FILTER (WHERE type = 'income'), 0),
			COUNT(*)
		FROM project_transactions
		WHERE project_id = $1 AND parent_project_transaction_id IS NULL`,
		projectID).Scan(&resp.TotalExpense, &resp.TotalIncome, &resp.TransactionCount)
	if err != nil {
		return nil, fmt.Errorf("summary: %w", err)
	}
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND status = 'active'`,
		projectID).Scan(&resp.MemberCount); err != nil {
		return nil, fmt.Errorf("member count: %w", err)
	}
	return resp, nil
}

// MemberByUserID resolves the project_member row matching (projectID, userID).
// Returns ErrNotMember if the caller isn't a member of the project.
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
