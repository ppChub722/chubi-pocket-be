package accounting

import (
	"context"
	"errors"
	"fmt"

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
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("forbidden")
	ErrDuplicateTag = errors.New("tag name already exists")
)

// =============================================================
// ACCOUNTS
// =============================================================

func (s *Store) CreateAccount(ctx context.Context, acc *Account) error {
	query := `
		INSERT INTO accounts (user_id, name, type, balance, currency, icon, color, is_active,
			credit_limit, statement_date, payment_due_date, minimum_payment, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING id
	`
	return s.db.QueryRow(ctx, query,
		acc.UserID, acc.Name, acc.Type, acc.Balance, acc.Currency, acc.Icon, acc.Color, acc.IsActive,
		acc.CreditLimit, acc.StatementDate, acc.PaymentDueDate, acc.MinimumPayment,
		acc.CreatedAt, acc.UpdatedAt,
	).Scan(&acc.ID)
}

func (s *Store) GetAccounts(ctx context.Context, userID int64, isActive *bool) ([]Account, error) {
	query := `SELECT id, user_id, name, type, balance, currency, icon, color, is_active,
		credit_limit, statement_date, payment_due_date, minimum_payment, created_at, updated_at
		FROM accounts WHERE user_id = $1`
	args := []interface{}{userID}

	if isActive != nil {
		query += " AND is_active = $2"
		args = append(args, *isActive)
	}
	query += " ORDER BY id ASC"

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.UserID, &a.Name, &a.Type, &a.Balance, &a.Currency,
			&a.Icon, &a.Color, &a.IsActive, &a.CreditLimit, &a.StatementDate,
			&a.PaymentDueDate, &a.MinimumPayment, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

func (s *Store) GetAccountByID(ctx context.Context, accountID int64) (*Account, error) {
	query := `SELECT id, user_id, name, type, balance, currency, icon, color, is_active,
		credit_limit, statement_date, payment_due_date, minimum_payment, created_at, updated_at
		FROM accounts WHERE id = $1`
	var a Account
	err := s.db.QueryRow(ctx, query, accountID).Scan(
		&a.ID, &a.UserID, &a.Name, &a.Type, &a.Balance, &a.Currency,
		&a.Icon, &a.Color, &a.IsActive, &a.CreditLimit, &a.StatementDate,
		&a.PaymentDueDate, &a.MinimumPayment, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

func (s *Store) UpdateAccount(ctx context.Context, accountID int64, req UpdateAccountRequest) (*Account, error) {
	query := `
		UPDATE accounts SET
			name = COALESCE($2, name),
			balance = COALESCE($3, balance),
			icon = COALESCE($4, icon),
			color = COALESCE($5, color),
			is_active = COALESCE($6, is_active),
			credit_limit = COALESCE($7, credit_limit),
			statement_date = COALESCE($8, statement_date),
			payment_due_date = COALESCE($9, payment_due_date),
			minimum_payment = COALESCE($10, minimum_payment),
			updated_at = NOW()
		WHERE id = $1
		RETURNING id, user_id, name, type, balance, currency, icon, color, is_active,
			credit_limit, statement_date, payment_due_date, minimum_payment, created_at, updated_at
	`
	var a Account
	err := s.db.QueryRow(ctx, query, accountID,
		req.Name, req.Balance, req.Icon, req.Color, req.IsActive,
		req.CreditLimit, req.StatementDate, req.PaymentDueDate, req.MinimumPayment,
	).Scan(&a.ID, &a.UserID, &a.Name, &a.Type, &a.Balance, &a.Currency,
		&a.Icon, &a.Color, &a.IsActive, &a.CreditLimit, &a.StatementDate,
		&a.PaymentDueDate, &a.MinimumPayment, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

func (s *Store) DeactivateAccount(ctx context.Context, accountID int64) error {
	query := `UPDATE accounts SET is_active = false, updated_at = NOW() WHERE id = $1`
	tag, err := s.db.Exec(ctx, query, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetAccountSummary(ctx context.Context, accountID int64, from, to *string) (*AccountSummary, error) {
	summary := &AccountSummary{AccountID: accountID}

	// Get balance
	err := s.db.QueryRow(ctx, `SELECT balance FROM accounts WHERE id = $1`, accountID).Scan(&summary.Balance)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	// Get totals
	query := `
		SELECT
			COALESCE(SUM(CASE WHEN type = 'income' THEN amount ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN type = 'expense' THEN amount ELSE 0 END), 0),
			COUNT(*)
		FROM transactions WHERE account_id = $1
	`
	args := []interface{}{accountID}
	argIdx := 2

	if from != nil {
		query += fmt.Sprintf(" AND date >= $%d", argIdx)
		args = append(args, *from)
		argIdx++
	}
	if to != nil {
		query += fmt.Sprintf(" AND date <= $%d", argIdx)
		args = append(args, *to)
	}

	err = s.db.QueryRow(ctx, query, args...).Scan(&summary.TotalIncome, &summary.TotalExpense, &summary.TransactionCount)
	if err != nil {
		return nil, err
	}

	return summary, nil
}

// =============================================================
// CATEGORIES
// =============================================================

func (s *Store) CreateCategory(ctx context.Context, cat *Category) error {
	query := `
		INSERT INTO categories (user_id, parent_id, name, type, icon, color, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id
	`
	return s.db.QueryRow(ctx, query,
		cat.UserID, cat.ParentID, cat.Name, cat.Type, cat.Icon, cat.Color, cat.IsActive,
		cat.CreatedAt, cat.UpdatedAt,
	).Scan(&cat.ID)
}

func (s *Store) GetCategories(ctx context.Context, userID int64, catType string) ([]Category, error) {
	query := `SELECT id, user_id, parent_id, name, type, icon, color, is_active, created_at, updated_at
		FROM categories WHERE user_id = $1`
	args := []interface{}{userID}

	if catType != "" {
		query += " AND type = $2"
		args = append(args, catType)
	}
	query += " ORDER BY type, name ASC"

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.UserID, &c.ParentID, &c.Name, &c.Type,
			&c.Icon, &c.Color, &c.IsActive, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		categories = append(categories, c)
	}
	return categories, nil
}

func (s *Store) GetCategoryByID(ctx context.Context, categoryID int64) (*Category, error) {
	query := `SELECT id, user_id, parent_id, name, type, icon, color, is_active, created_at, updated_at
		FROM categories WHERE id = $1`
	var c Category
	err := s.db.QueryRow(ctx, query, categoryID).Scan(
		&c.ID, &c.UserID, &c.ParentID, &c.Name, &c.Type,
		&c.Icon, &c.Color, &c.IsActive, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

func (s *Store) UpdateCategory(ctx context.Context, categoryID int64, req UpdateCategoryRequest) (*Category, error) {
	query := `
		UPDATE categories SET
			name = COALESCE($2, name),
			icon = COALESCE($3, icon),
			color = COALESCE($4, color),
			is_active = COALESCE($5, is_active),
			updated_at = NOW()
		WHERE id = $1
		RETURNING id, user_id, parent_id, name, type, icon, color, is_active, created_at, updated_at
	`
	var c Category
	err := s.db.QueryRow(ctx, query, categoryID, req.Name, req.Icon, req.Color, req.IsActive).Scan(
		&c.ID, &c.UserID, &c.ParentID, &c.Name, &c.Type,
		&c.Icon, &c.Color, &c.IsActive, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

func (s *Store) DeactivateCategory(ctx context.Context, categoryID int64) error {
	query := `UPDATE categories SET is_active = false, updated_at = NOW() WHERE id = $1`
	tag, err := s.db.Exec(ctx, query, categoryID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) HasChildCategories(ctx context.Context, categoryID int64) (bool, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM categories WHERE parent_id = $1`, categoryID).Scan(&count)
	return count > 0, err
}

// =============================================================
// TAGS
// =============================================================

func (s *Store) CreateTag(ctx context.Context, tag *Tag) error {
	query := `INSERT INTO tags (user_id, name, color, created_at) VALUES ($1, $2, $3, $4) RETURNING id`
	err := s.db.QueryRow(ctx, query, tag.UserID, tag.Name, tag.Color, tag.CreatedAt).Scan(&tag.ID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDuplicateTag
		}
		return err
	}
	return nil
}

func (s *Store) GetTags(ctx context.Context, userID int64) ([]Tag, error) {
	query := `SELECT id, user_id, name, color, created_at FROM tags WHERE user_id = $1 ORDER BY name ASC`
	rows, err := s.db.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Color, &t.CreatedAt); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, nil
}

func (s *Store) GetTagByID(ctx context.Context, tagID int64) (*Tag, error) {
	query := `SELECT id, user_id, name, color, created_at FROM tags WHERE id = $1`
	var t Tag
	err := s.db.QueryRow(ctx, query, tagID).Scan(&t.ID, &t.UserID, &t.Name, &t.Color, &t.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

func (s *Store) UpdateTag(ctx context.Context, tagID int64, req UpdateTagRequest) (*Tag, error) {
	query := `
		UPDATE tags SET name = COALESCE($2, name), color = COALESCE($3, color)
		WHERE id = $1
		RETURNING id, user_id, name, color, created_at
	`
	var t Tag
	err := s.db.QueryRow(ctx, query, tagID, req.Name, req.Color).Scan(
		&t.ID, &t.UserID, &t.Name, &t.Color, &t.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDuplicateTag
		}
		return nil, err
	}
	return &t, nil
}

func (s *Store) DeleteTag(ctx context.Context, tagID int64) error {
	// CASCADE will remove from transaction_tags automatically
	tag, err := s.db.Exec(ctx, `DELETE FROM tags WHERE id = $1`, tagID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// =============================================================
// TRANSACTIONS
// =============================================================

func (s *Store) CreateTransaction(ctx context.Context, txRecord *Transaction) error {
	dbTx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbTx.Rollback(ctx)

	queryInsert := `
		INSERT INTO transactions (user_id, account_id, category_id, amount, type, note, date,
			transfer_to_account_id, project_id, photo_url, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id
	`
	err = dbTx.QueryRow(ctx, queryInsert,
		txRecord.UserID, txRecord.AccountID, txRecord.CategoryID,
		txRecord.Amount, txRecord.Type, txRecord.Note, txRecord.Date,
		txRecord.TransferToAccountID, txRecord.ProjectID, txRecord.PhotoURL,
		txRecord.CreatedAt, txRecord.UpdatedAt,
	).Scan(&txRecord.ID)
	if err != nil {
		return fmt.Errorf("failed to insert transaction: %w", err)
	}

	// Update source account balance
	if txRecord.Type == "expense" || txRecord.Type == "transfer" {
		_, err = dbTx.Exec(ctx,
			`UPDATE accounts SET balance = balance - $1, updated_at = NOW() WHERE id = $2`,
			txRecord.Amount, txRecord.AccountID)
	} else {
		_, err = dbTx.Exec(ctx,
			`UPDATE accounts SET balance = balance + $1, updated_at = NOW() WHERE id = $2`,
			txRecord.Amount, txRecord.AccountID)
	}
	if err != nil {
		return fmt.Errorf("failed to update source balance: %w", err)
	}

	// For transfers, credit the destination account
	if txRecord.Type == "transfer" && txRecord.TransferToAccountID != nil {
		_, err = dbTx.Exec(ctx,
			`UPDATE accounts SET balance = balance + $1, updated_at = NOW() WHERE id = $2`,
			txRecord.Amount, *txRecord.TransferToAccountID)
		if err != nil {
			return fmt.Errorf("failed to update destination balance: %w", err)
		}
	}

	return dbTx.Commit(ctx)
}

func (s *Store) GetTransactions(ctx context.Context, userID int64, params map[string]interface{}) ([]Transaction, int, error) {
	query := `SELECT id, user_id, account_id, category_id, amount, type, note, date,
		transfer_to_account_id, project_id, recurring_id, photo_url, created_at, updated_at
		FROM transactions WHERE user_id = $1`
	countQuery := `SELECT COUNT(*) FROM transactions WHERE user_id = $1`
	args := []interface{}{userID}
	argIdx := 2

	// Apply filters
	if v, ok := params["account_id"]; ok {
		filter := fmt.Sprintf(" AND account_id = $%d", argIdx)
		query += filter
		countQuery += filter
		args = append(args, v)
		argIdx++
	}
	if v, ok := params["category_id"]; ok {
		filter := fmt.Sprintf(" AND category_id = $%d", argIdx)
		query += filter
		countQuery += filter
		args = append(args, v)
		argIdx++
	}
	if v, ok := params["type"]; ok {
		filter := fmt.Sprintf(" AND type = $%d", argIdx)
		query += filter
		countQuery += filter
		args = append(args, v)
		argIdx++
	}
	if v, ok := params["project_id"]; ok {
		filter := fmt.Sprintf(" AND project_id = $%d", argIdx)
		query += filter
		countQuery += filter
		args = append(args, v)
		argIdx++
	}
	if v, ok := params["from"]; ok {
		filter := fmt.Sprintf(" AND date >= $%d", argIdx)
		query += filter
		countQuery += filter
		args = append(args, v)
		argIdx++
	}
	if v, ok := params["to"]; ok {
		filter := fmt.Sprintf(" AND date <= $%d", argIdx)
		query += filter
		countQuery += filter
		args = append(args, v)
		argIdx++
	}

	// Count total
	var total int
	err := s.db.QueryRow(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Sort
	sort := "date DESC"
	if v, ok := params["sort"]; ok {
		switch v {
		case "date_asc":
			sort = "date ASC"
		case "amount_desc":
			sort = "amount DESC"
		case "amount_asc":
			sort = "amount ASC"
		}
	}
	query += " ORDER BY " + sort

	// Pagination
	perPage := 20
	page := 1
	if v, ok := params["per_page"]; ok {
		perPage = v.(int)
	}
	if v, ok := params["page"]; ok {
		page = v.(int)
	}
	offset := (page - 1) * perPage
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", perPage, offset)

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var transactions []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.UserID, &t.AccountID, &t.CategoryID, &t.Amount, &t.Type,
			&t.Note, &t.Date, &t.TransferToAccountID, &t.ProjectID, &t.RecurringID,
			&t.PhotoURL, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, 0, err
		}
		transactions = append(transactions, t)
	}
	return transactions, total, nil
}

func (s *Store) GetTransactionByID(ctx context.Context, txID int64) (*Transaction, error) {
	query := `SELECT id, user_id, account_id, category_id, amount, type, note, date,
		transfer_to_account_id, project_id, recurring_id, photo_url, created_at, updated_at
		FROM transactions WHERE id = $1`
	var t Transaction
	err := s.db.QueryRow(ctx, query, txID).Scan(
		&t.ID, &t.UserID, &t.AccountID, &t.CategoryID, &t.Amount, &t.Type,
		&t.Note, &t.Date, &t.TransferToAccountID, &t.ProjectID, &t.RecurringID,
		&t.PhotoURL, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

func (s *Store) UpdateTransaction(ctx context.Context, txID int64, oldAmount float64, oldType string, accountID int64, req UpdateTransactionRequest) (*Transaction, error) {
	dbTx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer dbTx.Rollback(ctx)

	// Reverse old balance effect
	if oldType == "expense" || oldType == "transfer" {
		_, err = dbTx.Exec(ctx, `UPDATE accounts SET balance = balance + $1, updated_at = NOW() WHERE id = $2`, oldAmount, accountID)
	} else {
		_, err = dbTx.Exec(ctx, `UPDATE accounts SET balance = balance - $1, updated_at = NOW() WHERE id = $2`, oldAmount, accountID)
	}
	if err != nil {
		return nil, err
	}

	// Update the transaction
	query := `
		UPDATE transactions SET
			amount = COALESCE($2, amount),
			date = COALESCE($3, date),
			category_id = COALESCE($4, category_id),
			note = COALESCE($5, note),
			project_id = COALESCE($6, project_id),
			updated_at = NOW()
		WHERE id = $1
		RETURNING id, user_id, account_id, category_id, amount, type, note, date,
			transfer_to_account_id, project_id, recurring_id, photo_url, created_at, updated_at
	`
	var t Transaction
	err = dbTx.QueryRow(ctx, query, txID, req.Amount, req.Date, req.CategoryID, req.Note, req.ProjectID).Scan(
		&t.ID, &t.UserID, &t.AccountID, &t.CategoryID, &t.Amount, &t.Type,
		&t.Note, &t.Date, &t.TransferToAccountID, &t.ProjectID, &t.RecurringID,
		&t.PhotoURL, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}

	// Apply new balance effect
	if t.Type == "expense" || t.Type == "transfer" {
		_, err = dbTx.Exec(ctx, `UPDATE accounts SET balance = balance - $1, updated_at = NOW() WHERE id = $2`, t.Amount, t.AccountID)
	} else {
		_, err = dbTx.Exec(ctx, `UPDATE accounts SET balance = balance + $1, updated_at = NOW() WHERE id = $2`, t.Amount, t.AccountID)
	}
	if err != nil {
		return nil, err
	}

	if err := dbTx.Commit(ctx); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) DeleteTransaction(ctx context.Context, tx *Transaction) error {
	dbTx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbTx.Rollback(ctx)

	// Reverse balance
	if tx.Type == "expense" || tx.Type == "transfer" {
		_, err = dbTx.Exec(ctx, `UPDATE accounts SET balance = balance + $1, updated_at = NOW() WHERE id = $2`, tx.Amount, tx.AccountID)
	} else {
		_, err = dbTx.Exec(ctx, `UPDATE accounts SET balance = balance - $1, updated_at = NOW() WHERE id = $2`, tx.Amount, tx.AccountID)
	}
	if err != nil {
		return err
	}

	// For transfers, reverse destination
	if tx.Type == "transfer" && tx.TransferToAccountID != nil {
		_, err = dbTx.Exec(ctx, `UPDATE accounts SET balance = balance - $1, updated_at = NOW() WHERE id = $2`, tx.Amount, *tx.TransferToAccountID)
		if err != nil {
			return err
		}
	}

	// Delete transaction (CASCADE removes transaction_tags)
	_, err = dbTx.Exec(ctx, `DELETE FROM transactions WHERE id = $1`, tx.ID)
	if err != nil {
		return err
	}

	return dbTx.Commit(ctx)
}

func (s *Store) HasSharedExpense(ctx context.Context, txID int64) (bool, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM shared_expenses WHERE transaction_id = $1`, txID).Scan(&count)
	return count > 0, err
}

func (s *Store) GetTransactionSummary(ctx context.Context, userID int64, from, to string, accountID *int64, groupBy string) (*TransactionSummary, error) {
	summary := &TransactionSummary{}

	query := `
		SELECT
			COALESCE(SUM(CASE WHEN type = 'income' THEN amount ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN type = 'expense' THEN amount ELSE 0 END), 0)
		FROM transactions WHERE user_id = $1 AND date >= $2 AND date <= $3
	`
	args := []interface{}{userID, from, to}
	argIdx := 4

	if accountID != nil {
		query += fmt.Sprintf(" AND account_id = $%d", argIdx)
		args = append(args, *accountID)
	}

	err := s.db.QueryRow(ctx, query, args...).Scan(&summary.TotalIncome, &summary.TotalExpense)
	if err != nil {
		return nil, err
	}
	summary.Net = summary.TotalIncome - summary.TotalExpense

	// By category breakdown
	if groupBy == "category" || groupBy == "" {
		catQuery := `
			SELECT c.id, c.name, COALESCE(SUM(t.amount), 0)
			FROM transactions t
			JOIN categories c ON t.category_id = c.id
			WHERE t.user_id = $1 AND t.date >= $2 AND t.date <= $3 AND t.category_id IS NOT NULL
			GROUP BY c.id, c.name ORDER BY SUM(t.amount) DESC
		`
		rows, err := s.db.Query(ctx, catQuery, userID, from, to)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var cs CategorySummary
			if err := rows.Scan(&cs.CategoryID, &cs.Name, &cs.Total); err != nil {
				return nil, err
			}
			summary.ByCategory = append(summary.ByCategory, cs)
		}
	}

	return summary, nil
}

// =============================================================
// TRANSACTION TAGS
// =============================================================

func (s *Store) AddTransactionTags(ctx context.Context, txID int64, tagIDs []int64) error {
	for _, tagID := range tagIDs {
		_, err := s.db.Exec(ctx,
			`INSERT INTO transaction_tags (transaction_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			txID, tagID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RemoveTransactionTag(ctx context.Context, txID, tagID int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM transaction_tags WHERE transaction_id = $1 AND tag_id = $2`, txID, tagID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ReplaceTransactionTags(ctx context.Context, txID int64, tagIDs []int64) error {
	dbTx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbTx.Rollback(ctx)

	_, err = dbTx.Exec(ctx, `DELETE FROM transaction_tags WHERE transaction_id = $1`, txID)
	if err != nil {
		return err
	}

	for _, tagID := range tagIDs {
		_, err = dbTx.Exec(ctx, `INSERT INTO transaction_tags (transaction_id, tag_id) VALUES ($1, $2)`, txID, tagID)
		if err != nil {
			return err
		}
	}

	return dbTx.Commit(ctx)
}

func (s *Store) GetTransactionTags(ctx context.Context, txID int64) ([]Tag, error) {
	query := `SELECT t.id, t.user_id, t.name, t.color, t.created_at
		FROM tags t JOIN transaction_tags tt ON t.id = tt.tag_id
		WHERE tt.transaction_id = $1 ORDER BY t.name`
	rows, err := s.db.Query(ctx, query, txID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Color, &t.CreatedAt); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, nil
}
