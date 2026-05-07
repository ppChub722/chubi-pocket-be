package scheduled_transactions

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
	ErrScheduleNotFound = errors.New("scheduled transaction not found")
	ErrAccountNotOwned  = errors.New("account does not belong to caller or is not active")
	ErrInvalidCategory  = errors.New("category is invalid (system, wrong type, or not owned)")
)

const scheduleColumns = `id, user_id, account_id, name, type, entry_type, amount,
	category_id, billing_cycle, next_billing_date::text, day_of_month, status, note,
	total_amount, down_payment, total_installments, remaining_installments, interest_rate,
	icon_code, logo_url, created_at, updated_at`

func scanSchedule(row pgx.Row) (*ScheduledTransaction, error) {
	var s ScheduledTransaction
	var iconBytes []byte
	if err := row.Scan(
		&s.ID, &s.UserID, &s.AccountID, &s.Name, &s.Type, &s.EntryType, &s.Amount,
		&s.CategoryID, &s.BillingCycle, &s.NextBillingDate, &s.DayOfMonth, &s.Status, &s.Note,
		&s.TotalAmount, &s.DownPayment, &s.TotalInstallments, &s.RemainingInstallments, &s.InterestRate,
		&iconBytes, &s.LogoURL, &s.CreatedAt, &s.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if iconBytes != nil {
		s.IconCode = new(shared.IconCode)
		if err := json.Unmarshal(iconBytes, s.IconCode); err != nil {
			return nil, fmt.Errorf("unmarshal icon_code: %w", err)
		}
	}
	return &s, nil
}

// VerifyAccountOwnership — non-archived account belonging to caller.
func (s *Store) VerifyAccountOwnership(
	ctx context.Context, userID, accountID uuid.UUID,
) error {
	var name string
	err := s.db.QueryRow(ctx,
		`SELECT name FROM accounts WHERE id = $1 AND user_id = $2 AND status = 'active'`,
		accountID, userID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccountNotOwned
	}
	if err != nil {
		return fmt.Errorf("verify account: %w", err)
	}
	return nil
}

// VerifyCategory checks (a) ownership, (b) type matches `expectedType`,
// (c) not a system category. category_id is optional; nil short-circuits
// to a successful return.
func (s *Store) VerifyCategory(
	ctx context.Context, userID uuid.UUID, categoryID *uuid.UUID, expectedType string,
) error {
	if categoryID == nil {
		return nil
	}
	var cType string
	var isSystem bool
	err := s.db.QueryRow(ctx,
		`SELECT type, is_system FROM categories WHERE id = $1 AND user_id = $2`,
		*categoryID, userID).Scan(&cType, &isSystem)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidCategory
	}
	if err != nil {
		return fmt.Errorf("category lookup: %w", err)
	}
	if isSystem || cType != expectedType {
		return ErrInvalidCategory
	}
	return nil
}

func (s *Store) Create(
	ctx context.Context, userID uuid.UUID, req CreateRequest, dayOfMonth int,
) (*ScheduledTransaction, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}

	var iconBytes []byte
	if req.IconCode != nil {
		iconBytes, err = json.Marshal(req.IconCode)
		if err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
	}

	q := `INSERT INTO scheduled_transactions
		(id, user_id, account_id, name, type, entry_type, amount, category_id,
		 billing_cycle, next_billing_date, day_of_month, note,
		 total_amount, down_payment, total_installments, remaining_installments, interest_rate,
		 icon_code, logo_url, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8,
				$9, $10::date, $11, $12,
				$13, $14, $15, $16, $17,
				$18, $19, $2, $2)
		RETURNING ` + scheduleColumns

	return scanSchedule(s.db.QueryRow(ctx, q,
		id, userID, req.AccountID, strings.TrimSpace(req.Name), req.Type, req.EntryType, req.Amount,
		req.CategoryID, req.BillingCycle, req.NextBillingDate, dayOfMonth, req.Note,
		req.TotalAmount, req.DownPayment, req.TotalInstallments, req.RemainingInstallments, req.InterestRate,
		iconBytes, req.LogoURL,
	))
}

func (s *Store) GetByID(
	ctx context.Context, userID, id uuid.UUID,
) (*ScheduledTransaction, error) {
	q := `SELECT ` + scheduleColumns + ` FROM scheduled_transactions
	       WHERE id = $1 AND user_id = $2`
	row, err := scanSchedule(s.db.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrScheduleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return row, nil
}

// GetByIDForUpdate locks the row inside a tx — used by generate-now so
// the schedule + dependent advance can't race.
func (s *Store) GetByIDForUpdate(
	ctx context.Context, tx pgx.Tx, userID, id uuid.UUID,
) (*ScheduledTransaction, error) {
	q := `SELECT ` + scheduleColumns + ` FROM scheduled_transactions
	       WHERE id = $1 AND user_id = $2 FOR UPDATE`
	row, err := scanSchedule(tx.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrScheduleNotFound
	}
	return row, err
}

func (s *Store) List(
	ctx context.Context, userID uuid.UUID, f ListFilter,
) ([]ScheduledTransaction, error) {
	whereClauses := []string{"user_id = $1"}
	args := []any{userID}

	switch strings.ToLower(strings.TrimSpace(f.Status)) {
	case "", "active":
		whereClauses = append(whereClauses, "status = 'active'")
	case "paused":
		whereClauses = append(whereClauses, "status = 'paused'")
	case "cancelled":
		whereClauses = append(whereClauses, "status = 'cancelled'")
	case "completed":
		whereClauses = append(whereClauses, "status = 'completed'")
	case "all":
		// no filter
	}
	if f.EntryType != "" && f.EntryType != "all" {
		args = append(args, f.EntryType)
		whereClauses = append(whereClauses, fmt.Sprintf("entry_type = $%d", len(args)))
	}
	if f.Type != "" && f.Type != "all" {
		args = append(args, f.Type)
		whereClauses = append(whereClauses, fmt.Sprintf("type = $%d", len(args)))
	}
	if f.AccountID != nil {
		args = append(args, *f.AccountID)
		whereClauses = append(whereClauses, fmt.Sprintf("account_id = $%d", len(args)))
	}

	q := `SELECT ` + scheduleColumns + ` FROM scheduled_transactions
	       WHERE ` + strings.Join(whereClauses, " AND ") + `
	       ORDER BY next_billing_date ASC, created_at DESC`

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()
	out := make([]ScheduledTransaction, 0)
	for rows.Next() {
		row, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, *row)
	}
	return out, rows.Err()
}

// Upcoming — active rows whose next_billing_date is within `days` from
// today. Uses CURRENT_DATE on the DB; fine for Phase 1c (Phase 3 will
// likely want per-tz windows).
func (s *Store) Upcoming(
	ctx context.Context, userID uuid.UUID, days int,
) ([]ScheduledTransaction, error) {
	q := `SELECT ` + scheduleColumns + ` FROM scheduled_transactions
	       WHERE user_id = $1 AND status = 'active'
	         AND next_billing_date BETWEEN CURRENT_DATE
	                                   AND (CURRENT_DATE + ($2 || ' days')::interval)::date
	       ORDER BY next_billing_date ASC`
	rows, err := s.db.Query(ctx, q, userID, days)
	if err != nil {
		return nil, fmt.Errorf("upcoming: %w", err)
	}
	defer rows.Close()
	out := make([]ScheduledTransaction, 0)
	for rows.Next() {
		row, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, *row)
	}
	return out, rows.Err()
}

func (s *Store) Update(
	ctx context.Context, userID, id uuid.UUID, req UpdateRequest, newDayOfMonth *int,
) (*ScheduledTransaction, error) {
	q := `UPDATE scheduled_transactions SET updated_by_user_id = $1`
	args := []any{userID}

	if req.Name != nil {
		args = append(args, strings.TrimSpace(*req.Name))
		q += fmt.Sprintf(", name = $%d", len(args))
	}
	if req.Amount != nil {
		args = append(args, *req.Amount)
		q += fmt.Sprintf(", amount = $%d", len(args))
	}
	if req.AccountID != nil {
		args = append(args, *req.AccountID)
		q += fmt.Sprintf(", account_id = $%d", len(args))
	}
	if req.CategoryID != nil {
		args = append(args, *req.CategoryID)
		q += fmt.Sprintf(", category_id = $%d", len(args))
	}
	if req.BillingCycle != nil {
		args = append(args, *req.BillingCycle)
		q += fmt.Sprintf(", billing_cycle = $%d", len(args))
	}
	if req.NextBillingDate != nil {
		args = append(args, *req.NextBillingDate)
		q += fmt.Sprintf(", next_billing_date = $%d::date", len(args))
	}
	if newDayOfMonth != nil {
		args = append(args, *newDayOfMonth)
		q += fmt.Sprintf(", day_of_month = $%d", len(args))
	}
	if req.Note != nil {
		args = append(args, *req.Note)
		q += fmt.Sprintf(", note = $%d", len(args))
	}
	if req.TotalAmount != nil {
		args = append(args, *req.TotalAmount)
		q += fmt.Sprintf(", total_amount = $%d", len(args))
	}
	if req.DownPayment != nil {
		args = append(args, *req.DownPayment)
		q += fmt.Sprintf(", down_payment = $%d", len(args))
	}
	if req.TotalInstallments != nil {
		args = append(args, *req.TotalInstallments)
		q += fmt.Sprintf(", total_installments = $%d", len(args))
	}
	if req.RemainingInstallments != nil {
		args = append(args, *req.RemainingInstallments)
		q += fmt.Sprintf(", remaining_installments = $%d", len(args))
	}
	if req.InterestRate != nil {
		args = append(args, *req.InterestRate)
		q += fmt.Sprintf(", interest_rate = $%d", len(args))
	}
	if req.IconCode != nil {
		b, err := json.Marshal(req.IconCode)
		if err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
		args = append(args, b)
		q += fmt.Sprintf(", icon_code = $%d::jsonb", len(args))
	}
	if req.LogoURL != nil {
		args = append(args, *req.LogoURL)
		q += fmt.Sprintf(", logo_url = $%d", len(args))
	}

	args = append(args, id)
	q += fmt.Sprintf(" WHERE id = $%d AND user_id = $1 RETURNING ", len(args)) + scheduleColumns

	row, err := scanSchedule(s.db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrScheduleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	return row, nil
}

// SetStatus is the dedicated transition path used by pause/resume/cancel
// and by the generate-now auto-completion. Caller has validated transition.
func (s *Store) SetStatus(
	ctx context.Context, userID, id uuid.UUID, status string,
) (*ScheduledTransaction, error) {
	q := `UPDATE scheduled_transactions
	         SET status = $1, updated_by_user_id = $2
	       WHERE id = $3 AND user_id = $2
	   RETURNING ` + scheduleColumns
	row, err := scanSchedule(s.db.QueryRow(ctx, q, status, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrScheduleNotFound
	}
	return row, err
}

// SetStatusInTx is the in-tx variant — used by generate-now to advance
// to 'completed' atomically with the final tx insert + balance update.
func (s *Store) SetStatusInTx(
	ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, status string,
) error {
	_, err := tx.Exec(ctx,
		`UPDATE scheduled_transactions
		    SET status = $1, updated_by_user_id = $2
		  WHERE id = $3 AND user_id = $2`,
		status, userID, id)
	if err != nil {
		return fmt.Errorf("set status tx: %w", err)
	}
	return nil
}

// AdvanceTx applies the result of one generation cycle inside the
// caller's tx: bump next_billing_date, decrement remaining_installments
// (if applicable). Returns updated row.
func (s *Store) AdvanceTx(
	ctx context.Context, tx pgx.Tx, userID, id uuid.UUID,
	nextBillingDate string, decrementInstallment bool,
) (*ScheduledTransaction, error) {
	q := `UPDATE scheduled_transactions
	         SET next_billing_date = $1::date,
	             remaining_installments = CASE WHEN $4::boolean
	                                            THEN GREATEST(0, COALESCE(remaining_installments, 0) - 1)
	                                            ELSE remaining_installments
	                                       END,
	             updated_by_user_id = $2
	       WHERE id = $3 AND user_id = $2
	   RETURNING ` + scheduleColumns
	row, err := scanSchedule(tx.QueryRow(ctx, q, nextBillingDate, userID, id, decrementInstallment))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrScheduleNotFound
	}
	return row, err
}

func (s *Store) Delete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM scheduled_transactions WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

// History returns generated transactions for a schedule.
func (s *Store) History(
	ctx context.Context, userID, id uuid.UUID,
) ([]HistoryItem, float64, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, amount, date::text, created_at
		   FROM transactions
		  WHERE user_id = $1 AND scheduled_transaction_id = $2
		  ORDER BY date DESC, created_at DESC`,
		userID, id)
	if err != nil {
		return nil, 0, fmt.Errorf("history: %w", err)
	}
	defer rows.Close()

	out := make([]HistoryItem, 0)
	var total float64
	for rows.Next() {
		var h HistoryItem
		if err := rows.Scan(&h.ID, &h.Amount, &h.Date, &h.GeneratedAt); err != nil {
			return nil, 0, fmt.Errorf("scan: %w", err)
		}
		out = append(out, h)
		total += h.Amount
	}
	return out, total, rows.Err()
}

// AccountSnapshot for embedding in views.
type AccountSnapshot struct {
	ID   uuid.UUID
	Name string
}

func (s *Store) AccountSnapshots(
	ctx context.Context, userID uuid.UUID, ids []uuid.UUID,
) (map[uuid.UUID]AccountSnapshot, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]AccountSnapshot{}, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT id, name FROM accounts WHERE user_id = $1 AND id = ANY($2::uuid[])`,
		userID, ids)
	if err != nil {
		return nil, fmt.Errorf("acct snapshots: %w", err)
	}
	defer rows.Close()
	out := make(map[uuid.UUID]AccountSnapshot, len(ids))
	for rows.Next() {
		var a AccountSnapshot
		if err := rows.Scan(&a.ID, &a.Name); err != nil {
			return nil, err
		}
		out[a.ID] = a
	}
	return out, rows.Err()
}

// CategorySnapshot for embedding in views.
type CategorySnapshot struct {
	ID   uuid.UUID
	Name string
}

func (s *Store) CategorySnapshots(
	ctx context.Context, userID uuid.UUID, ids []uuid.UUID,
) (map[uuid.UUID]CategorySnapshot, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]CategorySnapshot{}, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT id, name FROM categories WHERE user_id = $1 AND id = ANY($2::uuid[])`,
		userID, ids)
	if err != nil {
		return nil, fmt.Errorf("cat snapshots: %w", err)
	}
	defer rows.Close()
	out := make(map[uuid.UUID]CategorySnapshot, len(ids))
	for rows.Next() {
		var c CategorySnapshot
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return nil, err
		}
		out[c.ID] = c
	}
	return out, rows.Err()
}

// LockAccountTx — same shape as transactions.LockAccountsForUpdate.
func (s *Store) LockAccountTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) error {
	if _, err := tx.Exec(ctx,
		`SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, accountID); err != nil {
		return fmt.Errorf("lock account: %w", err)
	}
	return nil
}

// ApplyBalanceDeltaTx — duplicated from transactions module for in-tx use
// without importing the package (would create a cycle).
func (s *Store) ApplyBalanceDeltaTx(
	ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID, delta float64,
) (newBalance float64, err error) {
	err = tx.QueryRow(ctx, `
		UPDATE accounts SET balance = balance + $1, updated_by_user_id = $2
		 WHERE id = $3 AND user_id = $2
	 RETURNING balance`, delta, userID, accountID).Scan(&newBalance)
	if err != nil {
		err = fmt.Errorf("apply balance: %w", err)
	}
	return
}

// InsertGeneratedTxTx inserts a single transactions row tagged with
// scheduled_transaction_id. Caller has already locked + adjusted the
// account balance.
func (s *Store) InsertGeneratedTxTx(
	ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID,
	categoryID *uuid.UUID, txType string, amount float64, date string,
	note *string, scheduleID uuid.UUID,
) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("uuid: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO transactions
		(id, user_id, account_id, type, amount, category_id, date, note,
		 scheduled_transaction_id, created_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7::date, $8, $9, $2)`,
		id, userID, accountID, txType, amount, categoryID, date, note, scheduleID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert generated tx: %w", err)
	}
	return id, nil
}
