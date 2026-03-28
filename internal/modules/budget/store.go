package budget

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

var (
	ErrNotFound        = errors.New("not found")
	ErrForbidden       = errors.New("forbidden")
	ErrDuplicateBudget = errors.New("budget already exists for this category/period")
)

// =============================================================
// BUDGETS
// =============================================================

func (s *Store) CreateBudget(ctx context.Context, b *Budget) error {
	query := `INSERT INTO budgets (user_id, category_id, amount, period, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`
	err := s.db.QueryRow(ctx, query, b.UserID, b.CategoryID, b.Amount, b.Period, b.IsActive, b.CreatedAt, b.UpdatedAt).Scan(&b.ID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDuplicateBudget
		}
		return err
	}
	return nil
}

func (s *Store) GetBudgets(ctx context.Context, userID int64, isActive *bool, period *string) ([]BudgetWithSpending, error) {
	query := `SELECT b.id, b.user_id, b.category_id, b.amount, b.period, b.is_active, b.created_at, b.updated_at,
		c.id, c.name
		FROM budgets b JOIN categories c ON b.category_id = c.id
		WHERE b.user_id = $1`
	args := []interface{}{userID}
	argIdx := 2

	if isActive != nil {
		query += fmt.Sprintf(" AND b.is_active = $%d", argIdx)
		args = append(args, *isActive)
		argIdx++
	}
	if period != nil {
		query += fmt.Sprintf(" AND b.period = $%d", argIdx)
		args = append(args, *period)
	}
	query += " ORDER BY c.name"

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var budgets []BudgetWithSpending
	for rows.Next() {
		var b BudgetWithSpending
		if err := rows.Scan(&b.ID, &b.UserID, &b.CategoryID, &b.Amount, &b.Period, &b.IsActive,
			&b.CreatedAt, &b.UpdatedAt, &b.Category.ID, &b.Category.Name); err != nil {
			return nil, err
		}

		// Calculate spending for current period
		start, end := getPeriodBounds(b.Period)
		b.SpentThisPeriod = s.getCategorySpending(ctx, userID, b.CategoryID, start, end)
		b.Remaining = b.Amount - b.SpentThisPeriod
		if b.Amount > 0 {
			b.UtilizationPct = int((b.SpentThisPeriod / b.Amount) * 100)
		}
		budgets = append(budgets, b)
	}
	return budgets, nil
}

func (s *Store) GetBudgetByID(ctx context.Context, budgetID int64) (*BudgetDetail, error) {
	query := `SELECT b.id, b.user_id, b.category_id, b.amount, b.period, b.is_active, b.created_at, b.updated_at,
		c.id, c.name
		FROM budgets b JOIN categories c ON b.category_id = c.id WHERE b.id = $1`
	var b BudgetDetail
	err := s.db.QueryRow(ctx, query, budgetID).Scan(&b.ID, &b.UserID, &b.CategoryID, &b.Amount, &b.Period,
		&b.IsActive, &b.CreatedAt, &b.UpdatedAt, &b.Category.ID, &b.Category.Name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	start, end := getPeriodBounds(b.Period)
	b.CurrentPeriodStart = start
	b.CurrentPeriodEnd = end
	b.SpentThisPeriod = s.getCategorySpending(ctx, b.UserID, b.CategoryID, start, end)
	b.Remaining = b.Amount - b.SpentThisPeriod
	if b.Amount > 0 {
		b.UtilizationPct = int((b.SpentThisPeriod / b.Amount) * 100)
	}

	// Child breakdown
	childRows, err := s.db.Query(ctx, `
		SELECT c.name, COALESCE(SUM(t.amount), 0)
		FROM categories c
		LEFT JOIN transactions t ON t.category_id = c.id AND t.type = 'expense' AND t.date >= $2 AND t.date <= $3
		WHERE c.parent_id = $1 AND c.user_id = $4
		GROUP BY c.name ORDER BY SUM(t.amount) DESC NULLS LAST`,
		b.CategoryID, start, end, b.UserID)
	if err == nil {
		defer childRows.Close()
		for childRows.Next() {
			var cs ChildSpending
			childRows.Scan(&cs.Category, &cs.Spent)
			b.ChildBreakdown = append(b.ChildBreakdown, cs)
		}
	}

	return &b, nil
}

func (s *Store) UpdateBudget(ctx context.Context, budgetID int64, req UpdateBudgetRequest) error {
	query := `UPDATE budgets SET amount = COALESCE($2, amount), is_active = COALESCE($3, is_active), updated_at = NOW() WHERE id = $1`
	tag, err := s.db.Exec(ctx, query, budgetID, req.Amount, req.IsActive)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteBudget(ctx context.Context, budgetID int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM budgets WHERE id = $1`, budgetID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetBudgetOwnerID(ctx context.Context, budgetID int64) (int64, error) {
	var userID int64
	err := s.db.QueryRow(ctx, `SELECT user_id FROM budgets WHERE id = $1`, budgetID).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return userID, nil
}

func (s *Store) getCategorySpending(ctx context.Context, userID, categoryID int64, from, to string) float64 {
	var spent float64
	// Include child categories
	s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(t.amount), 0)
		FROM transactions t
		WHERE t.user_id = $1 AND t.type = 'expense' AND t.date >= $3 AND t.date <= $4
		AND (t.category_id = $2 OR t.category_id IN (SELECT id FROM categories WHERE parent_id = $2))`,
		userID, categoryID, from, to).Scan(&spent)
	return spent
}

// =============================================================
// SAVING GOALS
// =============================================================

func (s *Store) CreateSavingGoal(ctx context.Context, sg *SavingGoal) error {
	query := `INSERT INTO saving_goals (user_id, name, target_amount, current_amount, account_id, deadline, is_completed, icon, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`
	return s.db.QueryRow(ctx, query, sg.UserID, sg.Name, sg.TargetAmount, sg.CurrentAmount,
		sg.AccountID, sg.Deadline, sg.IsCompleted, sg.Icon, sg.CreatedAt, sg.UpdatedAt).Scan(&sg.ID)
}

func (s *Store) GetSavingGoals(ctx context.Context, userID int64, isCompleted *bool) ([]SavingGoal, error) {
	query := `SELECT id, user_id, name, target_amount, current_amount, account_id, deadline, is_completed, icon, created_at, updated_at
		FROM saving_goals WHERE user_id = $1`
	args := []interface{}{userID}
	if isCompleted != nil {
		query += " AND is_completed = $2"
		args = append(args, *isCompleted)
	}
	query += " ORDER BY created_at DESC"

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var goals []SavingGoal
	for rows.Next() {
		var sg SavingGoal
		if err := rows.Scan(&sg.ID, &sg.UserID, &sg.Name, &sg.TargetAmount, &sg.CurrentAmount,
			&sg.AccountID, &sg.Deadline, &sg.IsCompleted, &sg.Icon, &sg.CreatedAt, &sg.UpdatedAt); err != nil {
			return nil, err
		}
		if sg.TargetAmount > 0 {
			sg.ProgressPct = int((sg.CurrentAmount / sg.TargetAmount) * 100)
		}
		goals = append(goals, sg)
	}
	return goals, nil
}

func (s *Store) GetSavingGoalByID(ctx context.Context, goalID int64) (*SavingGoalDetail, error) {
	query := `SELECT id, user_id, name, target_amount, current_amount, account_id, deadline, is_completed, icon, created_at, updated_at
		FROM saving_goals WHERE id = $1`
	var sg SavingGoalDetail
	err := s.db.QueryRow(ctx, query, goalID).Scan(&sg.ID, &sg.UserID, &sg.Name, &sg.TargetAmount,
		&sg.CurrentAmount, &sg.AccountID, &sg.Deadline, &sg.IsCompleted, &sg.Icon, &sg.CreatedAt, &sg.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if sg.TargetAmount > 0 {
		sg.ProgressPct = int((sg.CurrentAmount / sg.TargetAmount) * 100)
	}

	// Calculate days remaining and required monthly
	if sg.Deadline != nil {
		deadline, err := time.Parse("2006-01-02", *sg.Deadline)
		if err == nil {
			days := int(time.Until(deadline).Hours() / 24)
			sg.DaysRemaining = &days
			months := float64(days) / 30.0
			if months > 0 {
				required := (sg.TargetAmount - sg.CurrentAmount) / months
				sg.RequiredMonthly = &required
			}
		}
	}

	// Get linked account info
	if sg.AccountID != nil {
		var acc AccountBrief
		err := s.db.QueryRow(ctx, `SELECT id, name, balance FROM accounts WHERE id = $1`, *sg.AccountID).Scan(&acc.ID, &acc.Name, &acc.Balance)
		if err == nil {
			sg.Account = &acc
		}
	}

	return &sg, nil
}

func (s *Store) UpdateSavingGoal(ctx context.Context, goalID int64, req UpdateSavingGoalRequest) error {
	query := `UPDATE saving_goals SET
		name = COALESCE($2, name),
		target_amount = COALESCE($3, target_amount),
		current_amount = COALESCE($4, current_amount),
		account_id = COALESCE($5, account_id),
		deadline = COALESCE($6, deadline),
		icon = COALESCE($7, icon),
		is_completed = COALESCE($8, is_completed),
		updated_at = NOW()
		WHERE id = $1`
	tag, err := s.db.Exec(ctx, query, goalID, req.Name, req.TargetAmount, req.CurrentAmount,
		req.AccountID, req.Deadline, req.Icon, req.IsCompleted)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteSavingGoal(ctx context.Context, goalID int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM saving_goals WHERE id = $1`, goalID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetSavingGoalOwnerID(ctx context.Context, goalID int64) (int64, error) {
	var userID int64
	err := s.db.QueryRow(ctx, `SELECT user_id FROM saving_goals WHERE id = $1`, goalID).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return userID, nil
}

// =============================================================
// HELPERS
// =============================================================

func getPeriodBounds(period string) (string, string) {
	now := time.Now()
	switch period {
	case "weekly":
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		start := now.AddDate(0, 0, -(weekday - 1))
		end := start.AddDate(0, 0, 6)
		return start.Format("2006-01-02"), end.Format("2006-01-02")
	case "yearly":
		start := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
		end := time.Date(now.Year(), 12, 31, 0, 0, 0, 0, now.Location())
		return start.Format("2006-01-02"), end.Format("2006-01-02")
	default: // monthly
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		end := start.AddDate(0, 1, -1)
		return start.Format("2006-01-02"), end.Format("2006-01-02")
	}
}
