package budgets

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
	ErrBudgetNotFound  = errors.New("budget not found")
	ErrDuplicateBudget = errors.New("active budget already exists for this category/period/scope")
	ErrInvalidCategory = errors.New("category is income-type, system, or not owned by caller")
	ErrProjectNotOwner = errors.New("only the project owner can manage project-scope budgets")
)

const budgetColumns = `id, user_id, category_id, scope, project_id,
	amount, period, currency, status, description, note,
	created_at, updated_at`

func scanBudget(row pgx.Row) (*Budget, error) {
	var b Budget
	if err := row.Scan(
		&b.ID, &b.UserID, &b.CategoryID, &b.Scope, &b.ProjectID,
		&b.Amount, &b.Period, &b.Currency, &b.Status, &b.Description, &b.Note,
		&b.CreatedAt, &b.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &b, nil
}

// CategoryRef — minimal category snapshot for budget read responses.
type CategoryRef struct {
	ID       uuid.UUID
	Name     string
	IconCode *shared.IconCode
	ParentID *uuid.UUID
	Type     string
	IsSystem bool
}

// VerifyExpenseCategory checks the category belongs to the caller, is
// expense-typed, and is not a system category. Returns the snapshot for
// embedding in the read response.
func (s *Store) VerifyExpenseCategory(
	ctx context.Context, userID, categoryID uuid.UUID,
) (*CategoryRef, error) {
	var c CategoryRef
	var iconBytes []byte
	err := s.db.QueryRow(ctx,
		`SELECT id, name, icon_code, parent_id, type, is_system
		   FROM categories
		  WHERE id = $1 AND user_id = $2`,
		categoryID, userID).
		Scan(&c.ID, &c.Name, &iconBytes, &c.ParentID, &c.Type, &c.IsSystem)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidCategory
	}
	if err != nil {
		return nil, fmt.Errorf("category lookup: %w", err)
	}
	if c.Type != "expense" || c.IsSystem {
		return nil, ErrInvalidCategory
	}
	if iconBytes != nil {
		c.IconCode = new(shared.IconCode)
		if err := json.Unmarshal(iconBytes, c.IconCode); err != nil {
			return nil, fmt.Errorf("unmarshal icon_code: %w", err)
		}
	}
	return &c, nil
}

// VerifyProjectOwner ensures the caller owns the project — only owners
// can manage project-scope budgets.
func (s *Store) VerifyProjectOwner(
	ctx context.Context, userID, projectID uuid.UUID,
) error {
	var ownerID uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT owner_user_id FROM projects WHERE id = $1`, projectID).
		Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProjectNotOwner
	}
	if err != nil {
		return fmt.Errorf("project lookup: %w", err)
	}
	if ownerID != userID {
		return ErrProjectNotOwner
	}
	return nil
}

func (s *Store) Create(
	ctx context.Context, userID uuid.UUID, req CreateRequest, currency string,
) (*Budget, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}

	q := `INSERT INTO budgets
		(id, user_id, category_id, scope, project_id, amount, period,
		 currency, description, note, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $2, $2)
		RETURNING ` + budgetColumns
	b, err := scanBudget(s.db.QueryRow(ctx, q,
		id, userID, req.CategoryID, req.Scope, req.ProjectID,
		req.Amount, req.Period, currency, req.Description, req.Note,
	))
	if err != nil {
		// Best-effort detection of the partial unique-index hit.
		if strings.Contains(err.Error(), "idx_budgets_user_category_period") {
			return nil, ErrDuplicateBudget
		}
		return nil, fmt.Errorf("insert: %w", err)
	}
	return b, nil
}

// GetByID — visibility rule:
//   - user-scope budget: only owner (user_id) can see
//   - project-scope budget: any active member of the project can see
//
// Mutation authorization is enforced at the service layer; this read
// gate just controls visibility (incl. callers who are project members
// but not the budget's user_id).
func (s *Store) GetByID(
	ctx context.Context, callerID, id uuid.UUID,
) (*Budget, error) {
	q := `SELECT ` + budgetColumns + ` FROM budgets b
	       WHERE b.id = $1
	         AND (b.user_id = $2
	              OR (b.scope = 'project'
	                  AND b.project_id IN (
	                      SELECT pm.project_id FROM project_members pm
	                       WHERE pm.user_id = $2 AND pm.status = 'active')))`
	b, err := scanBudget(s.db.QueryRow(ctx, q, id, callerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBudgetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return b, nil
}

func (s *Store) List(
	ctx context.Context, callerID uuid.UUID, f ListFilter,
) ([]Budget, error) {
	// Visibility: own user-scope budgets + project-scope budgets on
	// projects where the caller is an active member. Same gate as
	// GetByID — kept inline so the planner can reorder freely.
	whereClauses := []string{
		`(user_id = $1
		  OR (scope = 'project'
		      AND project_id IN (
		          SELECT pm.project_id FROM project_members pm
		           WHERE pm.user_id = $1 AND pm.status = 'active')))`,
	}
	args := []any{callerID}

	switch strings.ToLower(strings.TrimSpace(f.Status)) {
	case "", "active":
		whereClauses = append(whereClauses, "status = 'active'")
	case "archived":
		whereClauses = append(whereClauses, "status = 'archived'")
	case "all":
		// no filter
	}
	if f.Period != "" && f.Period != "all" {
		args = append(args, f.Period)
		whereClauses = append(whereClauses, fmt.Sprintf("period = $%d", len(args)))
	}
	switch strings.ToLower(strings.TrimSpace(f.Scope)) {
	case "user":
		whereClauses = append(whereClauses, "scope = 'user'")
	case "project":
		whereClauses = append(whereClauses, "scope = 'project'")
	}
	if f.ProjectID != nil {
		args = append(args, *f.ProjectID)
		whereClauses = append(whereClauses, fmt.Sprintf("project_id = $%d", len(args)))
	}

	q := `SELECT ` + budgetColumns + ` FROM budgets
	       WHERE ` + strings.Join(whereClauses, " AND ") + `
	       ORDER BY created_at DESC`

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()
	out := make([]Budget, 0)
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// Update — authorization is enforced upstream by the service. The SQL
// itself only matches by id; `actorUserID` is the caller stamped into
// `updated_by_user_id` (NOT the row's user_id, which may be a different
// project-creator when caller is the current project owner).
func (s *Store) Update(
	ctx context.Context, actorUserID, id uuid.UUID, req UpdateRequest,
) (*Budget, error) {
	q := `UPDATE budgets SET updated_by_user_id = $1`
	args := []any{actorUserID}

	if req.Amount != nil {
		args = append(args, *req.Amount)
		q += fmt.Sprintf(", amount = $%d", len(args))
	}
	if req.Period != nil {
		args = append(args, *req.Period)
		q += fmt.Sprintf(", period = $%d", len(args))
	}
	if req.Currency != nil {
		args = append(args, strings.ToUpper(*req.Currency))
		q += fmt.Sprintf(", currency = $%d", len(args))
	}
	if req.Description != nil {
		args = append(args, *req.Description)
		q += fmt.Sprintf(", description = $%d", len(args))
	}
	if req.Note != nil {
		args = append(args, *req.Note)
		q += fmt.Sprintf(", note = $%d", len(args))
	}

	args = append(args, id)
	q += fmt.Sprintf(" WHERE id = $%d RETURNING ", len(args)) + budgetColumns

	b, err := scanBudget(s.db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBudgetNotFound
	}
	if err != nil {
		// Period change can collide with the unique index.
		if strings.Contains(err.Error(), "idx_budgets_user_category_period") {
			return nil, ErrDuplicateBudget
		}
		return nil, fmt.Errorf("update: %w", err)
	}
	return b, nil
}

// SetStatus flips between active and archived. Authorization enforced
// upstream; SQL matches by id only and stamps `actorUserID` as the
// updater (which may differ from the row's user_id when the actor is a
// project owner editing a project-scope budget created by someone else).
func (s *Store) SetStatus(
	ctx context.Context, actorUserID, id uuid.UUID, status string,
) (*Budget, error) {
	q := `UPDATE budgets SET status = $1, updated_by_user_id = $2
	       WHERE id = $3
	   RETURNING ` + budgetColumns
	b, err := scanBudget(s.db.QueryRow(ctx, q, status, actorUserID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBudgetNotFound
	}
	if err != nil {
		// Restore can collide with the unique index if a sibling has
		// claimed the same (category, period, project_id) slot while
		// this row was archived.
		if strings.Contains(err.Error(), "idx_budgets_user_category_period") {
			return nil, ErrDuplicateBudget
		}
		return nil, fmt.Errorf("set status: %w", err)
	}
	return b, nil
}

// Delete — authorization upstream; SQL matches by id only.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM budgets WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrBudgetNotFound
	}
	return nil
}

// ComputeSpentResult — aggregated spend + per-descendant breakdown.
// Recursive CTE walks the category tree from the budget's anchor.
type ComputeSpentResult struct {
	Total          float64
	ChildBreakdown []ChildBreakdownRow
}

// ComputeSpent runs the recursive descendants CTE + aggregate. For
// project-scope budgets, restricts to that project_id; for user-scope,
// restricts project_id IS NULL (personal book only).
//
// Period bounds are inclusive on both sides (DATE comparison; DB stores
// transactions.date as DATE — no tz coercion at this layer).
func (s *Store) ComputeSpent(
	ctx context.Context, userID uuid.UUID, b *Budget, periodStart, periodEnd string,
) (*ComputeSpentResult, error) {
	args := []any{userID, b.CategoryID, periodStart, periodEnd}
	projectClause := "AND t.project_id IS NULL"
	if b.Scope == ScopeProject && b.ProjectID != nil {
		args = append(args, *b.ProjectID)
		projectClause = "AND t.project_id = $5"
	}

	q := `
		WITH RECURSIVE descendants AS (
			SELECT id, name FROM categories WHERE id = $2
			UNION ALL
			SELECT c.id, c.name FROM categories c
			JOIN descendants d ON c.parent_id = d.id
		),
		sums AS (
			SELECT t.category_id, COALESCE(SUM(t.amount), 0) AS spent
			FROM transactions t
			WHERE t.user_id = $1
			  AND t.type = 'expense'
			  AND t.category_id IN (SELECT id FROM descendants)
			  AND t.date BETWEEN $3::date AND $4::date
			  ` + projectClause + `
			GROUP BY t.category_id
		)
		SELECT d.id, d.name, COALESCE(s.spent, 0) AS spent
		FROM descendants d
		LEFT JOIN sums s ON s.category_id = d.id
		ORDER BY d.id`

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("compute spent: %w", err)
	}
	defer rows.Close()

	out := ComputeSpentResult{ChildBreakdown: make([]ChildBreakdownRow, 0)}
	for rows.Next() {
		var r ChildBreakdownRow
		if err := rows.Scan(&r.CategoryID, &r.Name, &r.Spent); err != nil {
			return nil, fmt.Errorf("scan breakdown: %w", err)
		}
		out.Total += r.Spent
		// Drop the anchor category from the breakdown — it's the budget
		// itself; the FE shows it as the heading row.
		if r.CategoryID == b.CategoryID {
			continue
		}
		// Skip zero-spent descendants to keep the response light.
		if r.Spent == 0 {
			continue
		}
		out.ChildBreakdown = append(out.ChildBreakdown, r)
	}
	return &out, rows.Err()
}

// GetUserTimezone reads `preferences->>'timezone'` from user_preferences.
// Returns "" if the row is missing or the key is absent — caller falls
// back to the default tz.
func (s *Store) GetUserTimezone(
	ctx context.Context, userID uuid.UUID,
) (string, error) {
	var tz *string
	err := s.db.QueryRow(ctx,
		`SELECT preferences->>'timezone' FROM user_preferences WHERE user_id = $1`,
		userID).Scan(&tz)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("user prefs: %w", err)
	}
	if tz == nil {
		return "", nil
	}
	return *tz, nil
}

// GetUserCurrency reads `preferences->>'currency'`. Empty string if missing.
func (s *Store) GetUserCurrency(
	ctx context.Context, userID uuid.UUID,
) (string, error) {
	var cur *string
	err := s.db.QueryRow(ctx,
		`SELECT preferences->>'currency' FROM user_preferences WHERE user_id = $1`,
		userID).Scan(&cur)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("user prefs: %w", err)
	}
	if cur == nil {
		return "", nil
	}
	return *cur, nil
}
