package project

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

var (
	ErrNotFound      = errors.New("not found")
	ErrForbidden     = errors.New("forbidden")
	ErrAlreadyMember = errors.New("user is already a member")
	ErrCannotRemoveOwner = errors.New("cannot remove project owner")
)

func (s *Store) CreateProject(ctx context.Context, p *Project) error {
	query := `INSERT INTO projects (user_id, name, type, budget_goal, start_date, end_date, status, description, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`
	return s.db.QueryRow(ctx, query, p.UserID, p.Name, p.Type, p.BudgetGoal,
		p.StartDate, p.EndDate, p.Status, p.Description, p.CreatedAt, p.UpdatedAt).Scan(&p.ID)
}

func (s *Store) AddMember(ctx context.Context, projectID, userID int64, role string) (*ProjectMember, error) {
	var pm ProjectMember
	query := `INSERT INTO project_members (project_id, user_id, role, joined_at)
		VALUES ($1, $2, $3, NOW()) RETURNING id, project_id, user_id, role, joined_at`
	err := s.db.QueryRow(ctx, query, projectID, userID, role).Scan(&pm.ID, &pm.ProjectID, &pm.UserID, &pm.Role, &pm.JoinedAt)
	if err != nil {
		return nil, err
	}
	// Get user name/email
	s.db.QueryRow(ctx, `SELECT username, email FROM users WHERE id = $1`, userID).Scan(&pm.Name, &pm.Email)
	return &pm, nil
}

func (s *Store) GetProjects(ctx context.Context, userID int64, status, projType *string, page, perPage int) ([]ProjectListItem, int, error) {
	baseQuery := `FROM projects p
		LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $1
		WHERE (p.user_id = $1 OR pm.user_id = $1)`
	args := []interface{}{userID}
	argIdx := 2

	if status != nil {
		baseQuery += fmt.Sprintf(" AND p.status = $%d", argIdx)
		args = append(args, *status)
		argIdx++
	}
	if projType != nil {
		baseQuery += fmt.Sprintf(" AND p.type = $%d", argIdx)
		args = append(args, *projType)
	}

	var total int
	s.db.QueryRow(ctx, "SELECT COUNT(DISTINCT p.id) "+baseQuery, args...).Scan(&total)

	selectQuery := fmt.Sprintf(`SELECT DISTINCT p.id, p.user_id, p.name, p.type, p.budget_goal, p.start_date, p.end_date,
		p.status, p.description, p.created_at, p.updated_at,
		COALESCE(pm.role, CASE WHEN p.user_id = $1 THEN 'owner' ELSE 'viewer' END),
		(SELECT COUNT(*) FROM project_members WHERE project_id = p.id),
		COALESCE((SELECT SUM(amount) FROM transactions WHERE project_id = p.id AND type = 'expense'), 0),
		COALESCE((SELECT SUM(amount) FROM transactions WHERE project_id = p.id AND type = 'income'), 0)
		%s ORDER BY p.created_at DESC LIMIT %d OFFSET %d`, baseQuery, perPage, (page-1)*perPage)

	rows, err := s.db.Query(ctx, selectQuery, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []ProjectListItem
	for rows.Next() {
		var p ProjectListItem
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &p.Type, &p.BudgetGoal,
			&p.StartDate, &p.EndDate, &p.Status, &p.Description, &p.CreatedAt, &p.UpdatedAt,
			&p.MyRole, &p.MemberCount, &p.TotalExpense, &p.TotalIncome); err != nil {
			return nil, 0, err
		}
		items = append(items, p)
	}
	return items, total, nil
}

func (s *Store) GetProjectByID(ctx context.Context, projectID int64) (*Project, error) {
	query := `SELECT id, user_id, name, type, budget_goal, start_date, end_date, status, description, created_at, updated_at
		FROM projects WHERE id = $1`
	var p Project
	err := s.db.QueryRow(ctx, query, projectID).Scan(&p.ID, &p.UserID, &p.Name, &p.Type, &p.BudgetGoal,
		&p.StartDate, &p.EndDate, &p.Status, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (s *Store) UpdateProject(ctx context.Context, projectID int64, req UpdateProjectRequest) error {
	query := `UPDATE projects SET
		name = COALESCE($2, name), type = COALESCE($3, type), budget_goal = COALESCE($4, budget_goal),
		start_date = COALESCE($5, start_date), end_date = COALESCE($6, end_date),
		status = COALESCE($7, status), description = COALESCE($8, description), updated_at = NOW()
		WHERE id = $1`
	tag, err := s.db.Exec(ctx, query, projectID, req.Name, req.Type, req.BudgetGoal,
		req.StartDate, req.EndDate, req.Status, req.Description)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteProject(ctx context.Context, projectID int64) error {
	// Check for linked transactions
	var count int
	s.db.QueryRow(ctx, `SELECT COUNT(*) FROM transactions WHERE project_id = $1`, projectID).Scan(&count)
	if count > 0 {
		return errors.New("project has linked transactions")
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetProjectSummary(ctx context.Context, projectID int64, groupBy string) (*ProjectSummary, error) {
	summary := &ProjectSummary{}
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN type='income' THEN amount ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN type='expense' THEN amount ELSE 0 END), 0),
			COUNT(*)
		FROM transactions WHERE project_id = $1`, projectID).Scan(&summary.TotalIncome, &summary.TotalExpense, &summary.TransactionCount)
	if err != nil {
		return nil, err
	}
	summary.Net = summary.TotalIncome - summary.TotalExpense

	if groupBy == "category" || groupBy == "" {
		rows, err := s.db.Query(ctx, `
			SELECT COALESCE(c.name, 'Uncategorized'), COALESCE(SUM(t.amount), 0), COUNT(*)
			FROM transactions t LEFT JOIN categories c ON t.category_id = c.id
			WHERE t.project_id = $1
			GROUP BY c.name ORDER BY SUM(t.amount) DESC`, projectID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var ct CategoryTotal
				rows.Scan(&ct.Category, &ct.Total, &ct.TransactionCount)
				summary.ByCategory = append(summary.ByCategory, ct)
			}
		}
	}

	return summary, nil
}

func (s *Store) GetMembers(ctx context.Context, projectID int64) ([]ProjectMember, error) {
	query := `SELECT pm.id, pm.project_id, pm.user_id, u.username, u.email, pm.role, pm.joined_at
		FROM project_members pm JOIN users u ON pm.user_id = u.id
		WHERE pm.project_id = $1 ORDER BY pm.joined_at`
	rows, err := s.db.Query(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []ProjectMember
	for rows.Next() {
		var m ProjectMember
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.UserID, &m.Name, &m.Email, &m.Role, &m.JoinedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, nil
}

func (s *Store) GetMemberRole(ctx context.Context, projectID, userID int64) (string, error) {
	var role string
	err := s.db.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID).Scan(&role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return role, nil
}

func (s *Store) UpdateMemberRole(ctx context.Context, projectID, userID int64, role string) error {
	tag, err := s.db.Exec(ctx, `UPDATE project_members SET role = $3 WHERE project_id = $1 AND user_id = $2`, projectID, userID, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RemoveMember(ctx context.Context, projectID, userID int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) HasAccess(ctx context.Context, projectID, userID int64) (bool, error) {
	// Owner or member
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
			SELECT id FROM projects WHERE id = $1 AND user_id = $2
			UNION ALL
			SELECT id FROM project_members WHERE project_id = $1 AND user_id = $2
		) x`, projectID, userID).Scan(&count)
	return count > 0, err
}

func (s *Store) IsOwner(ctx context.Context, projectID, userID int64) (bool, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM projects WHERE id = $1 AND user_id = $2`, projectID, userID).Scan(&count)
	return count > 0, err
}
