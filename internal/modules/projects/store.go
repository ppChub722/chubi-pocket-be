package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

func (s *Store) Pool() *pgxpool.Pool { return s.db }

var (
	ErrProjectNotFound      = errors.New("project not found")
	ErrNotOwner             = errors.New("only the project owner can perform this action")
	ErrProjectLocked        = errors.New("project is cancelled or archived; writes are blocked")
	ErrProjectNotActive     = errors.New("project is not active")
	ErrMemberNotFound       = errors.New("member not found")
	ErrMemberAlreadyExists  = errors.New("user is already a member of this project")
	ErrCannotRemoveOwner    = errors.New("cannot remove the project owner; transfer ownership first")
	ErrOwnerCannotLeave     = errors.New("project owner cannot leave; transfer ownership first")
	ErrNotMember            = errors.New("caller is not a member of this project")
	ErrPTNotFound           = errors.New("project transaction not found")
	ErrPTIsChild            = errors.New("cannot operate on a split-child row directly; edit/delete its parent")
	ErrSelfSplit            = errors.New("a split's member must differ from the parent's actor")
	ErrSplitsExceedParent   = errors.New("sum of splits exceeds parent amount")
	ErrMemberNotInProject   = errors.New("member does not belong to this project")
	ErrLinkRequestNotForYou = errors.New("link request is not addressed to caller")
	ErrCannotChangeOwner    = errors.New("the owner's role can't be changed here; use transfer-ownership")
	ErrViewerReadOnly       = errors.New("viewers can't change project transactions")
	ErrInvalidDateRange     = errors.New("end_date must not be before start_date")
	ErrBadMemberRequest     = errors.New("add a member with ad_hoc + display_name, or email + display_name")
	ErrBadPayback           = errors.New("a payback has exactly one split: the receiver, for the whole amount")
	ErrPayerFixed           = errors.New("a bill's payer can't change")
	// ErrPaybackNotCopyable — a payback goes to my books as a repayment, not a copied expense.
	ErrPaybackNotCopyable = errors.New("a payback can't be copied as an expense")
	// 0.4.1-2 V1 / V2: a completed event is locked for good.
	ErrEventCompleted      = errors.New("the event is completed: it can't be changed any more")
	ErrUseCompleteEndpoint = errors.New("complete an event with POST /projects/{id}/complete")
	ErrCompleteNotActive   = errors.New("only an active event can be completed")
)

// EmptyPersonError — an event can't be completed while a bill still has an empty "( ) ____" person
// (a removed member's spot, default 3).
type EmptyPersonError struct{ BillIDs []uuid.UUID }

func (e *EmptyPersonError) Error() string { return "a bill still has an empty person to assign" }

const projectColumns = `id, owner_user_id, name, description, note,
	to_char(start_date, 'YYYY-MM-DD') AS start_date,
	to_char(end_date, 'YYYY-MM-DD')   AS end_date,
	status, icon_code, planned_amount, created_at, updated_at, completed_at`

func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	var iconBytes []byte
	err := row.Scan(
		&p.ID, &p.OwnerUserID, &p.Name, &p.Description, &p.Note,
		&p.StartDate, &p.EndDate, &p.Status, &iconBytes, &p.PlannedAmount,
		&p.CreatedAt, &p.UpdatedAt, &p.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	p.IconCode = shared.IconFromJSON(iconBytes)
	return &p, nil
}

func checkDates(start, end *string) error {
	if start != nil && end != nil && *end < *start {
		return ErrInvalidDateRange
	}
	return nil
}

func (s *Store) CreateInTx(ctx context.Context, tx pgx.Tx, ownerUserID uuid.UUID, req CreateProjectRequest) (*Project, error) {
	if err := checkDates(req.StartDate, req.EndDate); err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	var iconJSON []byte
	if req.IconCode != nil {
		if iconJSON, err = json.Marshal(req.IconCode); err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
	}
	return scanProject(tx.QueryRow(ctx, `INSERT INTO projects
		(id, owner_user_id, name, description, note, start_date, end_date,
		 icon_code, planned_amount, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6::date, $7::date, $8::jsonb, $9, $2, $2)
		RETURNING `+projectColumns,
		id, ownerUserID, strings.TrimSpace(req.Name), shared.CleanText(req.Description), shared.CleanText(req.Note),
		req.StartDate, req.EndDate, iconJSON, req.PlannedAmount,
	))
}

func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*Project, error) {
	p, err := scanProject(s.db.QueryRow(ctx, `SELECT `+projectColumns+` FROM scopes WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return p, nil
}

func (s *Store) GetByIDForCaller(ctx context.Context, userID, projectID uuid.UUID) (*Project, error) {
	p, err := s.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	// 0.4.1-2 S1: a split bill's hidden "event" never opens as an event (only via /event-bills).
	var (
		kind string
		n    int
	)
	if err := s.db.QueryRow(ctx, `SELECT p.kind, (SELECT COUNT(*) FROM project_members
			WHERE scope_id = p.id AND user_id = $2 AND status = 'active')
		FROM scopes p WHERE p.id = $1`, projectID, userID).Scan(&kind, &n); err != nil {
		return nil, fmt.Errorf("member check: %w", err)
	}
	if kind != "event" || (p.OwnerUserID != userID && n == 0) {
		return nil, ErrProjectNotFound
	}
	return p, nil
}

func (s *Store) ListIDs(ctx context.Context, userID uuid.UUID, f ListFilter) ([]uuid.UUID, int, error) {
	where := []string{`p.kind = 'event'`, `(p.owner_user_id = $1 OR EXISTS (
		SELECT 1 FROM project_members m
		WHERE m.project_id = p.id AND m.user_id = $1 AND m.status = 'active'))`}
	args := []any{userID}
	switch strings.ToLower(strings.TrimSpace(f.Status)) {
	case "", StatusActive:
		where = append(where, "p.status = 'active'")
	case StatusCompleted, StatusCancelled, StatusArchived:
		args = append(args, strings.ToLower(strings.TrimSpace(f.Status)))
		where = append(where, fmt.Sprintf("p.status = $%d", len(args)))
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		args = append(args, "%"+shared.EscapeLike(q)+"%")
		where = append(where, fmt.Sprintf("p.name ILIKE $%d", len(args)))
	}
	if len(f.TagIDs) > 0 {
		args = append(args, f.TagIDs)
		where = append(where, fmt.Sprintf(`EXISTS (SELECT 1 FROM project_my_tags mt
			WHERE mt.project_id = p.id AND mt.user_id = $1 AND mt.tag_id = ANY($%d))`, len(args)))
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM projects p WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}
	order := "p.updated_at DESC"
	switch f.Sort {
	case "start_date_desc":
		order = "p.start_date DESC NULLS LAST, p.updated_at DESC"
	case "name_asc":
		order = "LOWER(p.name), p.updated_at DESC"
	}
	args = append(args, f.PerPage, (f.Page-1)*f.PerPage)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT p.id FROM projects p WHERE %s ORDER BY %s, p.id
		LIMIT $%d OFFSET $%d`, w, order, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()
	out := make([]uuid.UUID, 0, f.PerPage)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		out = append(out, id)
	}
	return out, total, rows.Err()
}

func (s *Store) UpdateTx(ctx context.Context, tx pgx.Tx, actorID, id uuid.UUID, current *Project, req UpdateProjectRequest) error {
	start, end := current.StartDate, current.EndDate
	if req.has("start_date") {
		start = req.StartDate
	}
	if req.has("end_date") {
		end = req.EndDate
	}
	if err := checkDates(start, end); err != nil {
		return err
	}
	q := `UPDATE projects SET updated_by_user_id = $1`
	args := []any{actorID}
	set := func(col string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(", %s = $%d", col, len(args))
	}
	if req.Name != nil {
		set("name", strings.TrimSpace(*req.Name))
	}
	if v, ok := req.DescriptionChange(); ok {
		set("description", v)
	}
	if v, ok := req.NoteChange(); ok {
		set("note", v)
	}
	if req.has("start_date") {
		args = append(args, req.StartDate)
		q += fmt.Sprintf(", start_date = $%d::date", len(args))
	}
	if req.has("end_date") {
		args = append(args, req.EndDate)
		q += fmt.Sprintf(", end_date = $%d::date", len(args))
	}
	if req.Status != nil {
		set("status", *req.Status)
	}
	if req.has("icon_code") {
		var b []byte
		if req.IconCode != nil {
			var err error
			if b, err = json.Marshal(req.IconCode); err != nil {
				return fmt.Errorf("marshal icon_code: %w", err)
			}
		}
		args = append(args, b)
		q += fmt.Sprintf(", icon_code = $%d::jsonb", len(args))
	}
	if req.has("planned_amount") {
		set("planned_amount", req.PlannedAmount)
	}
	args = append(args, id)
	q += fmt.Sprintf(" WHERE id = $%d", len(args))
	if _, err := tx.Exec(ctx, q, args...); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return nil
}

func (s *Store) DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE transactions SET project_id = NULL, source_project_transaction_id = NULL
		WHERE project_id = $1 OR source_project_transaction_id IN (SELECT id FROM project_transactions WHERE scope_id = $1)`, id); err != nil {
		return fmt.Errorf("unlink transactions: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debts SET project_id = NULL WHERE project_id = $1`, id); err != nil {
		return fmt.Errorf("unlink debts: %w", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM projects WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

func (s *Store) TransferOwnershipTx(
	ctx context.Context, tx pgx.Tx, projectID, currentOwner, newOwner uuid.UUID,
) error {
	if _, err := tx.Exec(ctx,
		`UPDATE project_members SET role = 'contributor', updated_by_user_id = $1
		WHERE project_id = $2 AND user_id = $3 AND role = 'owner'`,
		currentOwner, projectID, currentOwner); err != nil {
		return fmt.Errorf("demote owner: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE project_members SET role = 'owner', updated_by_user_id = $1
		WHERE project_id = $2 AND user_id = $3 AND status = 'active'`,
		currentOwner, projectID, newOwner)
	if err != nil {
		return fmt.Errorf("promote owner: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMemberNotFound
	}
	if _, err := tx.Exec(ctx,
		`UPDATE projects SET owner_user_id = $1, updated_by_user_id = $2 WHERE id = $3`,
		newOwner, currentOwner, projectID); err != nil {
		return fmt.Errorf("update owner_user_id: %w", err)
	}
	return nil
}

func (s *Store) AssertWritable(ctx context.Context, projectID uuid.UUID, op string) error {
	var status string
	if err := s.db.QueryRow(ctx,
		`SELECT status FROM scopes WHERE id = $1`, projectID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProjectNotFound
		}
		return fmt.Errorf("status check: %w", err)
	}
	return writable(status, op)
}

func writable(status, op string) error {
	switch op {
	case "create_pt":
		if status != StatusActive {
			if status == StatusCompleted {
				return ErrEventCompleted
			}
			return ErrProjectLocked
		}
	case "edit_pt", "member_crud": // V1: completed = locked for good
		switch status {
		case StatusCompleted:
			return ErrEventCompleted
		case StatusCancelled, StatusArchived:
			if op == "edit_pt" || status == StatusArchived {
				return ErrProjectLocked
			}
		}
	case "resolve": // my books of a bill: still allowed after completion (a one-time copy, no sync)
		if status == StatusArchived {
			return ErrProjectLocked
		}
	case "mark": // a board tick changes the bill row
		switch status {
		case StatusCompleted:
			return ErrEventCompleted
		case StatusArchived:
			return ErrProjectLocked
		}
	}
	return nil
}
