package shared

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
	ErrNotFound       = errors.New("not found")
	ErrAlreadyLinked  = errors.New("transaction already has a shared expense")
	ErrHasSettlements = errors.New("cannot delete: settlements exist")
	ErrOverpayment    = errors.New("amount exceeds outstanding balance")
)

// =============================================================
// SHARED EXPENSES
// =============================================================

func (s *Store) CreateSharedExpense(ctx context.Context, se *SharedExpense) error {
	query := `INSERT INTO shared_expenses (transaction_id, total_amount, description, created_at)
		VALUES ($1, $2, $3, $4) RETURNING id`
	return s.db.QueryRow(ctx, query, se.TransactionID, se.TotalAmount, se.Description, se.CreatedAt).Scan(&se.ID)
}

func (s *Store) IsTransactionLinked(ctx context.Context, txID int64) (bool, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM shared_expenses WHERE transaction_id = $1`, txID).Scan(&count)
	return count > 0, err
}

func (s *Store) GetSharedExpenses(ctx context.Context, userID int64, isSettled *bool, from, to *string, page, perPage int) ([]SharedExpenseListItem, int, error) {
	baseQuery := `FROM shared_expenses se
		JOIN transactions t ON se.transaction_id = t.id
		WHERE t.user_id = $1`
	args := []interface{}{userID}
	argIdx := 2

	if from != nil {
		baseQuery += fmt.Sprintf(" AND se.created_at >= $%d", argIdx)
		args = append(args, *from)
		argIdx++
	}
	if to != nil {
		baseQuery += fmt.Sprintf(" AND se.created_at <= $%d", argIdx)
		args = append(args, *to)
		argIdx++
	}

	// Count
	var total int
	err := s.db.QueryRow(ctx, "SELECT COUNT(*) "+baseQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	selectQuery := fmt.Sprintf(`SELECT se.id, se.transaction_id, se.total_amount, se.description, se.created_at,
		(SELECT COUNT(*) FROM shared_expense_splits WHERE shared_expense_id = se.id),
		(SELECT COUNT(*) FROM shared_expense_splits WHERE shared_expense_id = se.id AND is_settled = false)
		%s ORDER BY se.created_at DESC LIMIT %d OFFSET %d`, baseQuery, perPage, (page-1)*perPage)

	rows, err := s.db.Query(ctx, selectQuery, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []SharedExpenseListItem
	for rows.Next() {
		var item SharedExpenseListItem
		if err := rows.Scan(&item.ID, &item.TransactionID, &item.TotalAmount, &item.Description,
			&item.CreatedAt, &item.SplitsCount, &item.UnsettledCount); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, nil
}

func (s *Store) GetSharedExpenseByID(ctx context.Context, seID int64) (*SharedExpenseWithSplits, error) {
	query := `SELECT id, transaction_id, total_amount, description, created_at FROM shared_expenses WHERE id = $1`
	var se SharedExpenseWithSplits
	err := s.db.QueryRow(ctx, query, seID).Scan(&se.ID, &se.TransactionID, &se.TotalAmount, &se.Description, &se.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	splits, err := s.GetSplitsByExpenseID(ctx, seID)
	if err != nil {
		return nil, err
	}
	se.Splits = splits

	for _, sp := range splits {
		se.TotalSettled += sp.PaidAmount
		se.TotalOutstanding += sp.Outstanding
	}

	return &se, nil
}

func (s *Store) GetSharedExpenseOwnerID(ctx context.Context, seID int64) (int64, error) {
	var userID int64
	err := s.db.QueryRow(ctx, `SELECT t.user_id FROM shared_expenses se JOIN transactions t ON se.transaction_id = t.id WHERE se.id = $1`, seID).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return userID, nil
}

func (s *Store) UpdateSharedExpense(ctx context.Context, seID int64, description *string) error {
	tag, err := s.db.Exec(ctx, `UPDATE shared_expenses SET description = COALESCE($2, description) WHERE id = $1`, seID, description)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteSharedExpense(ctx context.Context, seID int64) error {
	// Check for settlements
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM split_settlements ss JOIN shared_expense_splits sp ON ss.split_id = sp.id WHERE sp.shared_expense_id = $1`, seID).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrHasSettlements
	}

	tag, err := s.db.Exec(ctx, `DELETE FROM shared_expenses WHERE id = $1`, seID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// =============================================================
// SPLITS
// =============================================================

func (s *Store) CreateSplit(ctx context.Context, split *Split) error {
	query := `INSERT INTO shared_expense_splits (shared_expense_id, person_name, owed_amount, paid_amount, is_settled, created_at, updated_at)
		VALUES ($1, $2, $3, 0, false, $4, $5) RETURNING id`
	return s.db.QueryRow(ctx, query, split.SharedExpenseID, split.PersonName, split.OwedAmount, split.CreatedAt, split.UpdatedAt).Scan(&split.ID)
}

func (s *Store) GetSplitsByExpenseID(ctx context.Context, seID int64) ([]Split, error) {
	query := `SELECT id, shared_expense_id, person_name, owed_amount, paid_amount, is_settled, created_at, updated_at
		FROM shared_expense_splits WHERE shared_expense_id = $1 ORDER BY id`
	rows, err := s.db.Query(ctx, query, seID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var splits []Split
	for rows.Next() {
		var sp Split
		if err := rows.Scan(&sp.ID, &sp.SharedExpenseID, &sp.PersonName, &sp.OwedAmount,
			&sp.PaidAmount, &sp.IsSettled, &sp.CreatedAt, &sp.UpdatedAt); err != nil {
			return nil, err
		}
		sp.Outstanding = sp.OwedAmount - sp.PaidAmount
		splits = append(splits, sp)
	}
	return splits, nil
}

func (s *Store) GetSplitByID(ctx context.Context, splitID int64) (*Split, error) {
	query := `SELECT id, shared_expense_id, person_name, owed_amount, paid_amount, is_settled, created_at, updated_at
		FROM shared_expense_splits WHERE id = $1`
	var sp Split
	err := s.db.QueryRow(ctx, query, splitID).Scan(&sp.ID, &sp.SharedExpenseID, &sp.PersonName,
		&sp.OwedAmount, &sp.PaidAmount, &sp.IsSettled, &sp.CreatedAt, &sp.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	sp.Outstanding = sp.OwedAmount - sp.PaidAmount
	return &sp, nil
}

func (s *Store) GetSplitOwnerID(ctx context.Context, splitID int64) (int64, error) {
	var userID int64
	err := s.db.QueryRow(ctx, `
		SELECT t.user_id FROM shared_expense_splits sp
		JOIN shared_expenses se ON sp.shared_expense_id = se.id
		JOIN transactions t ON se.transaction_id = t.id
		WHERE sp.id = $1`, splitID).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return userID, nil
}

func (s *Store) UpdateSplit(ctx context.Context, splitID int64, req UpdateSplitRequest) (*Split, error) {
	query := `UPDATE shared_expense_splits SET
		person_name = COALESCE($2, person_name),
		owed_amount = COALESCE($3, owed_amount),
		updated_at = NOW()
		WHERE id = $1
		RETURNING id, shared_expense_id, person_name, owed_amount, paid_amount, is_settled, created_at, updated_at`
	var sp Split
	err := s.db.QueryRow(ctx, query, splitID, req.PersonName, req.OwedAmount).Scan(
		&sp.ID, &sp.SharedExpenseID, &sp.PersonName, &sp.OwedAmount, &sp.PaidAmount, &sp.IsSettled, &sp.CreatedAt, &sp.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	sp.Outstanding = sp.OwedAmount - sp.PaidAmount
	return &sp, nil
}

func (s *Store) DeleteSplit(ctx context.Context, splitID int64) error {
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM split_settlements WHERE split_id = $1`, splitID).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrHasSettlements
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM shared_expense_splits WHERE id = $1`, splitID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// =============================================================
// SETTLEMENTS
// =============================================================

func (s *Store) CreateSettlement(ctx context.Context, settlement *Settlement) (*Split, error) {
	dbTx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer dbTx.Rollback(ctx)

	// Get current split
	split, err := s.GetSplitByID(ctx, settlement.SplitID)
	if err != nil {
		return nil, err
	}

	// Check overpayment
	if split.PaidAmount+settlement.Amount > split.OwedAmount {
		return nil, ErrOverpayment
	}

	// Insert settlement
	err = dbTx.QueryRow(ctx,
		`INSERT INTO split_settlements (split_id, amount, settled_date, note, created_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		settlement.SplitID, settlement.Amount, settlement.SettledDate, settlement.Note, settlement.CreatedAt,
	).Scan(&settlement.ID)
	if err != nil {
		return nil, err
	}

	// Update split
	newPaid := split.PaidAmount + settlement.Amount
	isSettled := newPaid >= split.OwedAmount
	_, err = dbTx.Exec(ctx,
		`UPDATE shared_expense_splits SET paid_amount = $2, is_settled = $3, updated_at = NOW() WHERE id = $1`,
		split.ID, newPaid, isSettled)
	if err != nil {
		return nil, err
	}

	if err := dbTx.Commit(ctx); err != nil {
		return nil, err
	}

	// Return updated split
	split.PaidAmount = newPaid
	split.IsSettled = isSettled
	split.Outstanding = split.OwedAmount - newPaid
	return split, nil
}

func (s *Store) GetSettlements(ctx context.Context, splitID int64) ([]Settlement, error) {
	query := `SELECT id, split_id, amount, settled_date, note, created_at FROM split_settlements WHERE split_id = $1 ORDER BY settled_date`
	rows, err := s.db.Query(ctx, query, splitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var settlements []Settlement
	for rows.Next() {
		var st Settlement
		if err := rows.Scan(&st.ID, &st.SplitID, &st.Amount, &st.SettledDate, &st.Note, &st.CreatedAt); err != nil {
			return nil, err
		}
		settlements = append(settlements, st)
	}
	return settlements, nil
}

func (s *Store) GetSettlementByID(ctx context.Context, settlementID int64) (*Settlement, error) {
	query := `SELECT id, split_id, amount, settled_date, note, created_at FROM split_settlements WHERE id = $1`
	var st Settlement
	err := s.db.QueryRow(ctx, query, settlementID).Scan(&st.ID, &st.SplitID, &st.Amount, &st.SettledDate, &st.Note, &st.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &st, nil
}

func (s *Store) GetSettlementOwnerID(ctx context.Context, settlementID int64) (int64, error) {
	var userID int64
	err := s.db.QueryRow(ctx, `
		SELECT t.user_id FROM split_settlements ss
		JOIN shared_expense_splits sp ON ss.split_id = sp.id
		JOIN shared_expenses se ON sp.shared_expense_id = se.id
		JOIN transactions t ON se.transaction_id = t.id
		WHERE ss.id = $1`, settlementID).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return userID, nil
}

func (s *Store) DeleteSettlement(ctx context.Context, settlementID int64) error {
	dbTx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbTx.Rollback(ctx)

	// Get settlement
	st, err := s.GetSettlementByID(ctx, settlementID)
	if err != nil {
		return err
	}

	// Delete settlement
	_, err = dbTx.Exec(ctx, `DELETE FROM split_settlements WHERE id = $1`, settlementID)
	if err != nil {
		return err
	}

	// Recalculate split
	_, err = dbTx.Exec(ctx, `
		UPDATE shared_expense_splits SET
			paid_amount = paid_amount - $2,
			is_settled = CASE WHEN (paid_amount - $2) >= owed_amount THEN true ELSE false END,
			updated_at = NOW()
		WHERE id = $1`, st.SplitID, st.Amount)
	if err != nil {
		return err
	}

	return dbTx.Commit(ctx)
}

func (s *Store) GetSummary(ctx context.Context, userID int64) (*SharedExpenseSummary, error) {
	summary := &SharedExpenseSummary{}

	query := `
		SELECT
			COALESCE(SUM(sp.owed_amount - sp.paid_amount) FILTER (WHERE sp.is_settled = false), 0),
			COUNT(*) FILTER (WHERE sp.is_settled = false),
			COUNT(*) FILTER (WHERE sp.is_settled = true)
		FROM shared_expense_splits sp
		JOIN shared_expenses se ON sp.shared_expense_id = se.id
		JOIN transactions t ON se.transaction_id = t.id
		WHERE t.user_id = $1
	`
	err := s.db.QueryRow(ctx, query, userID).Scan(&summary.TotalOwedToMe, &summary.OpenSplitsCount, &summary.SettledSplitsCount)
	if err != nil {
		return nil, err
	}
	return summary, nil
}
