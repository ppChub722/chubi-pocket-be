package accounting

import (
	"context"
	"errors"
	"time"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

// ---------------------------------------------------------
// 1. ACCOUNTS (Wallets)
// ---------------------------------------------------------

func (s *Service) CreateAccount(ctx context.Context, userID int64, req CreateAccountRequest) (*Account, error) {
	acc := &Account{
		UserID:         userID,
		Name:           req.Name,
		Type:           req.Type,
		Currency:       req.Currency,
		InitialBalance: req.InitialBalance,
		Color:          req.Color,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := s.store.CreateAccount(ctx, acc); err != nil {
		return nil, err
	}

	return acc, nil
}

func (s *Service) GetMyAccounts(ctx context.Context, userID int64) ([]Account, error) {
	return s.store.GetAccounts(ctx, userID)
}

// ---------------------------------------------------------
// 2. CATEGORIES
// ---------------------------------------------------------

func (s *Service) CreateCategory(ctx context.Context, userID int64, req CreateCategoryRequest) (*Category, error) {
	cat := &Category{
		UserID:    userID,
		Name:      req.Name,
		Type:      req.Type,
		ParentID:  req.ParentID,
		Icon:      req.Icon,
		Color:     req.Color,
		CreatedAt: time.Now(),
	}

	if err := s.store.CreateCategory(ctx, cat); err != nil {
		return nil, err
	}

	return cat, nil
}

func (s *Service) GetMyCategories(ctx context.Context, userID int64) ([]Category, error) {
	return s.store.GetCategories(ctx, userID, "")
}

// ---------------------------------------------------------
// 3. TRANSACTIONS (The Complex Logic)
// ---------------------------------------------------------

func (s *Service) CreateTransaction(ctx context.Context, userID int64, req CreateTransactionRequest) (*Transaction, error) {
	// 1. Business Logic: Handle Negative Numbers
	// If it's an EXPENSE, we must make the amount negative (-100)
	// If it's an INCOME, we keep it positive (+100)
	finalAmount := req.Amount
	if req.Type == "expense" {
		finalAmount = -req.Amount
	}

	// 2. Parse Date String (ISO 8601)
	// Frontend sends "2025-11-25T10:30:00Z"
	parsedTime, err := time.Parse(time.RFC3339, req.TransactionDate)
	if err != nil {
		return nil, errors.New("invalid date format, use ISO8601 (e.g., 2023-01-01T12:00:00Z)")
	}

	// 3. Prepare Model
	tx := &Transaction{
		UserID:          userID,
		AccountID:       req.AccountID,
		CategoryID:      req.CategoryID,
		Amount:          finalAmount,
		Type:            req.Type,
		Description:     req.Description,
		TransactionDate: parsedTime,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	// 4. Save to DB (Store will handle atomic balance update)
	if err := s.store.CreateTransaction(ctx, tx); err != nil {
		return nil, err
	}

	return tx, nil
}

func (s *Service) GetMyTransactions(ctx context.Context, userID int64) ([]Transaction, error) {
	return s.store.GetTransactions(ctx, userID, 20) // Default limit 20
}