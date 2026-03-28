package recurring

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
)

func (s *Store) CreateRecurring(ctx context.Context, r *Recurring) error {
	query := `INSERT INTO recurrings (user_id, account_id, name, type, entry_type, amount, category_id,
		billing_cycle, next_billing_date, status, total_amount, down_payment, monthly_payment,
		total_installments, remaining_installments, note, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) RETURNING id`
	return s.db.QueryRow(ctx, query,
		r.UserID, r.AccountID, r.Name, r.Type, r.EntryType, r.Amount, r.CategoryID,
		r.BillingCycle, r.NextBillingDate, r.Status, r.TotalAmount, r.DownPayment, r.MonthlyPayment,
		r.TotalInstallments, r.RemainingInstallments, r.Note, r.CreatedAt, r.UpdatedAt,
	).Scan(&r.ID)
}

func (s *Store) GetRecurrings(ctx context.Context, userID int64, entryType, status, txType, billingCycle *string, page, perPage int) ([]RecurringListItem, int, error) {
	baseQuery := `FROM recurrings r WHERE r.user_id = $1`
	args := []interface{}{userID}
	argIdx := 2

	if entryType != nil {
		baseQuery += fmt.Sprintf(" AND r.entry_type = $%d", argIdx)
		args = append(args, *entryType)
		argIdx++
	}
	if status != nil {
		baseQuery += fmt.Sprintf(" AND r.status = $%d", argIdx)
		args = append(args, *status)
		argIdx++
	}
	if txType != nil {
		baseQuery += fmt.Sprintf(" AND r.type = $%d", argIdx)
		args = append(args, *txType)
		argIdx++
	}
	if billingCycle != nil {
		baseQuery += fmt.Sprintf(" AND r.billing_cycle = $%d", argIdx)
		args = append(args, *billingCycle)
	}

	var total int
	s.db.QueryRow(ctx, "SELECT COUNT(*) "+baseQuery, args...).Scan(&total)

	selectQuery := fmt.Sprintf(`SELECT r.id, r.user_id, r.account_id, r.name, r.type, r.entry_type, r.amount,
		r.category_id, r.billing_cycle, r.next_billing_date, r.status,
		r.total_amount, r.down_payment, r.monthly_payment, r.total_installments, r.remaining_installments,
		r.note, r.created_at, r.updated_at,
		(SELECT c.name FROM categories c WHERE c.id = r.category_id),
		(SELECT a.name FROM accounts a WHERE a.id = r.account_id)
		%s ORDER BY r.next_billing_date ASC LIMIT %d OFFSET %d`, baseQuery, perPage, (page-1)*perPage)

	rows, err := s.db.Query(ctx, selectQuery, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []RecurringListItem
	for rows.Next() {
		var r RecurringListItem
		var catName, accName *string
		if err := rows.Scan(&r.ID, &r.UserID, &r.AccountID, &r.Name, &r.Type, &r.EntryType, &r.Amount,
			&r.CategoryID, &r.BillingCycle, &r.NextBillingDate, &r.Status,
			&r.TotalAmount, &r.DownPayment, &r.MonthlyPayment, &r.TotalInstallments, &r.RemainingInstallments,
			&r.Note, &r.CreatedAt, &r.UpdatedAt, &catName, &accName); err != nil {
			return nil, 0, err
		}
		if catName != nil {
			r.Category = &CategoryBrief{Name: *catName}
		}
		if accName != nil {
			r.Account = &AccountBrief{Name: *accName}
		}
		if r.EntryType == "installment" && r.TotalInstallments != nil && *r.TotalInstallments > 0 {
			remaining := 0
			if r.RemainingInstallments != nil {
				remaining = *r.RemainingInstallments
			}
			pct := int(float64(*r.TotalInstallments-remaining) / float64(*r.TotalInstallments) * 100)
			r.ProgressPct = &pct
		}
		items = append(items, r)
	}
	return items, total, nil
}

func (s *Store) GetRecurringByID(ctx context.Context, recurringID int64) (*Recurring, error) {
	query := `SELECT id, user_id, account_id, name, type, entry_type, amount, category_id,
		billing_cycle, next_billing_date, status, total_amount, down_payment, monthly_payment,
		total_installments, remaining_installments, note, created_at, updated_at
		FROM recurrings WHERE id = $1`
	var r Recurring
	err := s.db.QueryRow(ctx, query, recurringID).Scan(&r.ID, &r.UserID, &r.AccountID, &r.Name, &r.Type,
		&r.EntryType, &r.Amount, &r.CategoryID, &r.BillingCycle, &r.NextBillingDate, &r.Status,
		&r.TotalAmount, &r.DownPayment, &r.MonthlyPayment, &r.TotalInstallments, &r.RemainingInstallments,
		&r.Note, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &r, nil
}

func (s *Store) UpdateRecurring(ctx context.Context, recurringID int64, req UpdateRecurringRequest) error {
	query := `UPDATE recurrings SET
		name = COALESCE($2, name), amount = COALESCE($3, amount), category_id = COALESCE($4, category_id),
		next_billing_date = COALESCE($5, next_billing_date), note = COALESCE($6, note), updated_at = NOW()
		WHERE id = $1`
	tag, err := s.db.Exec(ctx, query, recurringID, req.Name, req.Amount, req.CategoryID, req.NextBillingDate, req.Note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteRecurring(ctx context.Context, recurringID int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM recurrings WHERE id = $1`, recurringID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateStatus(ctx context.Context, recurringID int64, status string) error {
	tag, err := s.db.Exec(ctx, `UPDATE recurrings SET status = $2, updated_at = NOW() WHERE id = $1`, recurringID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetUpcoming(ctx context.Context, userID int64, days int) ([]UpcomingItem, float64, error) {
	endDate := time.Now().AddDate(0, 0, days).Format("2006-01-02")
	query := `SELECT r.id, r.user_id, r.account_id, r.name, r.type, r.entry_type, r.amount,
		r.category_id, r.billing_cycle, r.next_billing_date, r.status,
		r.total_amount, r.down_payment, r.monthly_payment, r.total_installments, r.remaining_installments,
		r.note, r.created_at, r.updated_at,
		(SELECT a.name FROM accounts a WHERE a.id = r.account_id)
		FROM recurrings r WHERE r.user_id = $1 AND r.status = 'active' AND r.next_billing_date <= $2
		ORDER BY r.next_billing_date ASC`

	rows, err := s.db.Query(ctx, query, userID, endDate)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []UpcomingItem
	var totalDue float64
	now := time.Now()

	for rows.Next() {
		var r UpcomingItem
		var accName *string
		if err := rows.Scan(&r.ID, &r.UserID, &r.AccountID, &r.Name, &r.Type, &r.EntryType, &r.Amount,
			&r.CategoryID, &r.BillingCycle, &r.NextBillingDate, &r.Status,
			&r.TotalAmount, &r.DownPayment, &r.MonthlyPayment, &r.TotalInstallments, &r.RemainingInstallments,
			&r.Note, &r.CreatedAt, &r.UpdatedAt, &accName); err != nil {
			return nil, 0, err
		}
		if accName != nil {
			r.Account = &AccountBrief{Name: *accName}
		}
		billingDate, _ := time.Parse("2006-01-02", r.NextBillingDate)
		r.DaysUntil = int(billingDate.Sub(now).Hours() / 24)
		totalDue += r.Amount
		items = append(items, r)
	}
	return items, totalDue, nil
}

// ProcessDueRecurrings is the scheduler logic
func (s *Store) ProcessDueRecurrings(ctx context.Context) (int, error) {
	today := time.Now().Format("2006-01-02")

	rows, err := s.db.Query(ctx, `
		SELECT id, user_id, account_id, name, type, entry_type, amount, category_id,
			billing_cycle, next_billing_date, remaining_installments
		FROM recurrings WHERE status = 'active' AND next_billing_date <= $1`, today)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	processed := 0
	for rows.Next() {
		var id, userID, accountID int64
		var name, txType, entryType, billingCycle, nextBillingDate string
		var amount float64
		var categoryID *int64
		var remainingInstallments *int

		if err := rows.Scan(&id, &userID, &accountID, &name, &txType, &entryType, &amount,
			&categoryID, &billingCycle, &nextBillingDate, &remainingInstallments); err != nil {
			continue
		}

		dbTx, err := s.db.Begin(ctx)
		if err != nil {
			continue
		}

		// Create transaction
		note := fmt.Sprintf("%s — auto-generated", name)
		_, err = dbTx.Exec(ctx, `
			INSERT INTO transactions (user_id, account_id, category_id, amount, type, note, date, recurring_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())`,
			userID, accountID, categoryID, amount, txType, note, nextBillingDate, id)
		if err != nil {
			dbTx.Rollback(ctx)
			continue
		}

		// Update account balance
		if txType == "expense" {
			dbTx.Exec(ctx, `UPDATE accounts SET balance = balance - $1, updated_at = NOW() WHERE id = $2`, amount, accountID)
		} else {
			dbTx.Exec(ctx, `UPDATE accounts SET balance = balance + $1, updated_at = NOW() WHERE id = $2`, amount, accountID)
		}

		// Advance next_billing_date
		nextDate := advanceDate(nextBillingDate, billingCycle)
		updateQuery := `UPDATE recurrings SET next_billing_date = $2, updated_at = NOW()`
		updateArgs := []interface{}{id, nextDate}

		// Handle installments
		if entryType == "installment" && remainingInstallments != nil {
			newRemaining := *remainingInstallments - 1
			if newRemaining <= 0 {
				updateQuery += `, status = 'completed', remaining_installments = 0`
			} else {
				updateQuery += fmt.Sprintf(`, remaining_installments = %d`, newRemaining)
			}
		}
		updateQuery += ` WHERE id = $1`

		dbTx.Exec(ctx, updateQuery, updateArgs...)
		if err := dbTx.Commit(ctx); err == nil {
			processed++
		}
	}
	return processed, nil
}

func advanceDate(dateStr, cycle string) string {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return dateStr
	}
	switch cycle {
	case "daily":
		t = t.AddDate(0, 0, 1)
	case "weekly":
		t = t.AddDate(0, 0, 7)
	case "monthly":
		t = t.AddDate(0, 1, 0)
	case "yearly":
		t = t.AddDate(1, 0, 0)
	}
	return t.Format("2006-01-02")
}
