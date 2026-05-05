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
	ErrProjectHasTxs        = errors.New("project has transactions; cannot delete")
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
)

const projectColumns = `id, owner_user_id, name, type, description,
	to_char(start_date, 'YYYY-MM-DD') AS start_date,
	to_char(end_date, 'YYYY-MM-DD')   AS end_date,
	status, icon_code, created_at, updated_at`

func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	var iconBytes []byte
	err := row.Scan(
		&p.ID, &p.OwnerUserID, &p.Name, &p.Type, &p.Description,
		&p.StartDate, &p.EndDate, &p.Status, &iconBytes,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if iconBytes != nil {
		p.IconCode = new(shared.IconCode)
		if err := json.Unmarshal(iconBytes, p.IconCode); err != nil {
			return nil, fmt.Errorf("unmarshal icon_code: %w", err)
		}
	}
	return &p, nil
}

func (s *Store) Create(ctx context.Context, ownerUserID uuid.UUID, req CreateProjectRequest) (*Project, error) {
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
	q := `INSERT INTO projects
		(id, owner_user_id, name, type, description, start_date, end_date,
		 icon_code, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6::date, $7::date, $8::jsonb, $2, $2)
		RETURNING ` + projectColumns
	return scanProject(s.db.QueryRow(ctx, q,
		id, ownerUserID, strings.TrimSpace(req.Name), req.Type, req.Description,
		req.StartDate, req.EndDate, iconJSON,
	))
}

// CreateInTx is the same as Create but inside a caller-provided tx — used so
// the project insert + the owner's project_member row land atomically.
func (s *Store) CreateInTx(ctx context.Context, tx pgx.Tx, ownerUserID uuid.UUID, req CreateProjectRequest) (*Project, error) {
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
	q := `INSERT INTO projects
		(id, owner_user_id, name, type, description, start_date, end_date,
		 icon_code, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6::date, $7::date, $8::jsonb, $2, $2)
		RETURNING ` + projectColumns
	return scanProject(tx.QueryRow(ctx, q,
		id, ownerUserID, strings.TrimSpace(req.Name), req.Type, req.Description,
		req.StartDate, req.EndDate, iconJSON,
	))
}

func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*Project, error) {
	// Same projection as ListForUser so the FE sees `members_count`
	// consistently across create / list / get. Without this, the create
	// response omits the count (default 0) and the FE list shows
	// "0 members" until the next list refresh.
	q := `SELECT p.id, p.owner_user_id, p.name, p.type, p.description,
	             to_char(p.start_date, 'YYYY-MM-DD'),
	             to_char(p.end_date, 'YYYY-MM-DD'),
	             p.status, p.icon_code, p.created_at, p.updated_at,
	             (SELECT COUNT(*) FROM project_members
	              WHERE project_id = p.id AND status != 'left') AS members_count
	      FROM projects p WHERE p.id = $1`
	row := s.db.QueryRow(ctx, q, id)
	var p Project
	var iconBytes []byte
	err := row.Scan(
		&p.ID, &p.OwnerUserID, &p.Name, &p.Type, &p.Description,
		&p.StartDate, &p.EndDate, &p.Status, &iconBytes,
		&p.CreatedAt, &p.UpdatedAt,
		&p.MembersCount,
	)
	if iconBytes != nil {
		p.IconCode = new(shared.IconCode)
		if err2 := json.Unmarshal(iconBytes, p.IconCode); err2 != nil && err == nil {
			err = fmt.Errorf("unmarshal icon_code: %w", err2)
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return &p, nil
}

// GetByIDForCaller checks that caller is a member (or the owner) before
// returning the row. Non-members get ErrProjectNotFound to avoid info-leak.
func (s *Store) GetByIDForCaller(ctx context.Context, userID, projectID uuid.UUID) (*Project, error) {
	p, err := s.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID == userID {
		return p, nil
	}
	var n int
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM project_members
		WHERE project_id = $1 AND user_id = $2 AND status = 'active'`,
		projectID, userID).Scan(&n); err != nil {
		return nil, fmt.Errorf("member check: %w", err)
	}
	if n == 0 {
		return nil, ErrProjectNotFound
	}
	return p, nil
}

// ListForUser returns projects the caller owns OR is an active member of.
func (s *Store) ListForUser(ctx context.Context, userID uuid.UUID, f ListFilter) ([]Project, int, error) {
	whereClauses := []string{
		`(p.owner_user_id = $1 OR EXISTS (
			SELECT 1 FROM project_members m
			WHERE m.project_id = p.id AND m.user_id = $1 AND m.status = 'active'
		))`,
	}
	args := []any{userID}

	switch strings.ToLower(strings.TrimSpace(f.Status)) {
	case "", "active":
		whereClauses = append(whereClauses, "p.status = 'active'")
	case "completed":
		whereClauses = append(whereClauses, "p.status = 'completed'")
	case "cancelled":
		whereClauses = append(whereClauses, "p.status = 'cancelled'")
	case "archived":
		whereClauses = append(whereClauses, "p.status = 'archived'")
	case "all":
		// no filter
	}

	if f.Type != nil {
		args = append(args, *f.Type)
		whereClauses = append(whereClauses, fmt.Sprintf("p.type = $%d", len(args)))
	}

	where := strings.Join(whereClauses, " AND ")

	var total int
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM projects p WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}

	args = append(args, f.PerPage)
	limitIdx := len(args)
	args = append(args, (f.Page-1)*f.PerPage)
	offsetIdx := len(args)

	q := fmt.Sprintf(`
		SELECT p.id, p.owner_user_id, p.name, p.type, p.description,
		       to_char(p.start_date, 'YYYY-MM-DD'),
		       to_char(p.end_date, 'YYYY-MM-DD'),
		       p.status, p.icon_code, p.created_at, p.updated_at,
		       (SELECT COUNT(*) FROM project_members WHERE project_id = p.id AND status != 'left')
		FROM projects p
		WHERE %s
		ORDER BY p.updated_at DESC
		LIMIT $%d OFFSET $%d`, where, limitIdx, offsetIdx)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	out := make([]Project, 0, f.PerPage)
	for rows.Next() {
		var p Project
		var iconBytes []byte
		if err := rows.Scan(
			&p.ID, &p.OwnerUserID, &p.Name, &p.Type, &p.Description,
			&p.StartDate, &p.EndDate, &p.Status, &iconBytes,
			&p.CreatedAt, &p.UpdatedAt,
			&p.MembersCount,
		); err != nil {
			return nil, 0, fmt.Errorf("scan: %w", err)
		}
		if iconBytes != nil {
			p.IconCode = new(shared.IconCode)
			if err := json.Unmarshal(iconBytes, p.IconCode); err != nil {
				return nil, 0, fmt.Errorf("unmarshal icon_code: %w", err)
			}
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

func (s *Store) Update(ctx context.Context, ownerUserID, id uuid.UUID, req UpdateProjectRequest) (*Project, error) {
	q := `UPDATE projects SET updated_by_user_id = $1`
	args := []any{ownerUserID}

	if req.Name != nil {
		args = append(args, strings.TrimSpace(*req.Name))
		q += fmt.Sprintf(", name = $%d", len(args))
	}
	if req.Type != nil {
		args = append(args, *req.Type)
		q += fmt.Sprintf(", type = $%d", len(args))
	}
	if req.Description != nil {
		args = append(args, *req.Description)
		q += fmt.Sprintf(", description = $%d", len(args))
	}
	if req.StartDate != nil {
		args = append(args, *req.StartDate)
		q += fmt.Sprintf(", start_date = $%d::date", len(args))
	}
	if req.EndDate != nil {
		args = append(args, *req.EndDate)
		q += fmt.Sprintf(", end_date = $%d::date", len(args))
	}
	if req.Status != nil {
		args = append(args, *req.Status)
		q += fmt.Sprintf(", status = $%d", len(args))
	}
	if req.IconCode != nil {
		b, err := json.Marshal(req.IconCode)
		if err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
		args = append(args, b)
		q += fmt.Sprintf(", icon_code = $%d::jsonb", len(args))
	}
	args = append(args, id)
	q += fmt.Sprintf(" WHERE id = $%d AND owner_user_id = $1 RETURNING ", len(args)) + projectColumns

	p, err := scanProject(s.db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	return p, nil
}

// HasTransactions reports whether any project_transactions OR personal
// transactions reference this project.
func (s *Store) HasTransactions(ctx context.Context, projectID uuid.UUID) (bool, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT (
			(SELECT COUNT(*) FROM project_transactions WHERE project_id = $1)
			+ (SELECT COUNT(*) FROM transactions WHERE project_id = $1)
		)`, projectID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("count: %w", err)
	}
	return n > 0, nil
}

func (s *Store) Delete(ctx context.Context, ownerUserID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM projects WHERE id = $1 AND owner_user_id = $2`, id, ownerUserID)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// TransferOwnership atomically swaps the project's owner_user_id and the
// owner role on project_members. Both old and new owners must be linked
// active members.
func (s *Store) TransferOwnershipTx(
	ctx context.Context, tx pgx.Tx, projectID, currentOwner, newOwner uuid.UUID,
) error {
	// Demote current owner's member row.
	if _, err := tx.Exec(ctx,
		`UPDATE project_members SET role = 'contributor', updated_by_user_id = $1
		WHERE project_id = $2 AND user_id = $3 AND role = 'owner'`,
		currentOwner, projectID, currentOwner); err != nil {
		return fmt.Errorf("demote owner: %w", err)
	}
	// Promote new owner.
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
	// Update projects.owner_user_id.
	if _, err := tx.Exec(ctx,
		`UPDATE projects SET owner_user_id = $1, updated_by_user_id = $2
		WHERE id = $3`,
		newOwner, currentOwner, projectID); err != nil {
		return fmt.Errorf("update owner_user_id: %w", err)
	}
	return nil
}

// AssertWritable enforces the lifecycle gate per spec §10.3.1.
//   - active     → all writes OK
//   - completed  → resolve actions OK, new project_transactions blocked
//   - cancelled  → only resolve actions OK
//   - archived   → fully read-only
//
// `op` selects the policy:
//   "create_pt"  blocks if !active
//   "edit_pt"    blocks if cancelled || archived
//   "resolve"    blocks if archived
//   "member_crud" blocks if archived (cancelled/completed = owner-only — caller checks)
func (s *Store) AssertWritable(ctx context.Context, projectID uuid.UUID, op string) error {
	var status string
	if err := s.db.QueryRow(ctx,
		`SELECT status FROM projects WHERE id = $1`, projectID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProjectNotFound
		}
		return fmt.Errorf("status check: %w", err)
	}
	switch op {
	case "create_pt":
		if status != StatusActive {
			if status == StatusCompleted {
				return ErrProjectNotActive
			}
			return ErrProjectLocked
		}
	case "edit_pt":
		if status == StatusCancelled || status == StatusArchived {
			return ErrProjectLocked
		}
	case "resolve":
		if status == StatusArchived {
			return ErrProjectLocked
		}
	case "member_crud":
		if status == StatusArchived {
			return ErrProjectLocked
		}
	}
	return nil
}
