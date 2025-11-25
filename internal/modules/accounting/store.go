package accounting

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

// ---------------------------------------------------------
// 1. ACCOUNTS (Wallets)
// ---------------------------------------------------------

func (s *Store) CreateAccount(ctx context.Context, acc *Account) error {
	query := `
		INSERT INTO accounts (user_id, name, type, currency, initial_balance, current_balance, color, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id
	`
	// Current balance starts equal to initial balance
	acc.CurrentBalance = acc.InitialBalance

	return s.db.QueryRow(ctx, query,
		acc.UserID, acc.Name, acc.Type, acc.Currency,
		acc.InitialBalance, acc.CurrentBalance, acc.Color,
		acc.CreatedAt, acc.UpdatedAt,
	).Scan(&acc.ID)
}

func (s *Store) GetAccounts(ctx context.Context, userID int64) ([]Account, error) {
	query := `SELECT id, user_id, name, type, currency, initial_balance, current_balance, color, created_at, updated_at FROM accounts WHERE user_id = $1 ORDER BY id ASC`
	
	rows, err := s.db.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.UserID, &a.Name, &a.Type, &a.Currency, &a.InitialBalance, &a.CurrentBalance, &a.Color, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

func (s *Store) GetAccountByID(ctx context.Context, accountID int64) (*Account, error) {
	query := `SELECT id, user_id, name, initial_balance, current_balance, currency, created_at, updated_at FROM accounts WHERE id = $1`
	var a Account
	err := s.db.QueryRow(ctx, query, accountID).Scan(&a.ID, &a.UserID, &a.Name, &a.InitialBalance, &a.CurrentBalance, &a.Currency, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ---------------------------------------------------------
// 2. CATEGORIES
// ---------------------------------------------------------

func (s *Store) CreateCategory(ctx context.Context, cat *Category) error {
	query := `
		INSERT INTO categories (user_id, parent_id, name, type, icon, color, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`
	return s.db.QueryRow(ctx, query,
		cat.UserID, cat.ParentID, cat.Name, cat.Type, cat.Icon, cat.Color,
		cat.CreatedAt, cat.UpdatedAt,
	).Scan(&cat.ID)
}

func (s *Store) GetCategories(ctx context.Context, userID int64, catType string) ([]Category, error) {
	// If catType is empty, return all. If set, filter by 'income' or 'expense'
	query := `SELECT id, user_id, parent_id, name, type, icon, color, created_at, updated_at FROM categories WHERE user_id = $1`
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
		if err := rows.Scan(&c.ID, &c.UserID, &c.ParentID, &c.Name, &c.Type, &c.Icon, &c.Color, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		categories = append(categories, c)
	}
	return categories, nil
}

// ---------------------------------------------------------
// 3. TRANSACTIONS (With Atomic Balance Update)
// ---------------------------------------------------------

func (s *Store) CreateTransaction(ctx context.Context, txRecord *Transaction) error {
	// 1. Start a Database Transaction (Atomic Safety)
	// Everything below this happens together, or not at all.
	dbTx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	// Auto-rollback if we error out before committing
	defer dbTx.Rollback(ctx)

	// 2. Insert the Transaction Record
	queryInsert := `
		INSERT INTO transactions (user_id, account_id, category_id, amount, type, description, transaction_date, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id
	`
	err = dbTx.QueryRow(ctx, queryInsert,
		txRecord.UserID, txRecord.AccountID, txRecord.CategoryID,
		txRecord.Amount, txRecord.Type, txRecord.Description, txRecord.TransactionDate,
		txRecord.CreatedAt, txRecord.UpdatedAt,
	).Scan(&txRecord.ID)

	if err != nil {
		return fmt.Errorf("failed to insert transaction: %w", err)
	}

	// 3. Update the Account Balance
	// Postgres Logic: current_balance = current_balance + (new_amount)
	// If amount is negative (expense), it subtracts. If positive (income), it adds.
	queryUpdateBalance := `
		UPDATE accounts 
		SET current_balance = current_balance + $1, updated_at = NOW()
		WHERE id = $2
	`
	_, err = dbTx.Exec(ctx, queryUpdateBalance, txRecord.Amount, txRecord.AccountID)
	if err != nil {
		return fmt.Errorf("failed to update wallet balance: %w", err)
	}

	// 4. Commit (Save Changes)
	return dbTx.Commit(ctx)
}

func (s *Store) GetTransactions(ctx context.Context, userID int64, limit int) ([]Transaction, error) {
	query := `
		SELECT id, account_id, category_id, amount, type, description, transaction_date, created_at, updated_at
		FROM transactions 
		WHERE user_id = $1 
		ORDER BY transaction_date DESC 
		LIMIT $2
	`
	rows, err := s.db.Query(ctx, query, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var transactions []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.AccountID, &t.CategoryID, &t.Amount, &t.Type, &t.Description, &t.TransactionDate, &t.CreatedAt, &t.UpdatedAt,); err != nil {
			return nil, err
		}
		transactions = append(transactions, t)
	}
	return transactions, nil
}