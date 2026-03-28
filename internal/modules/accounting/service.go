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

// =============================================================
// ACCOUNTS
// =============================================================

func (s *Service) CreateAccount(ctx context.Context, userID int64, req CreateAccountRequest) (*Account, error) {
	currency := req.Currency
	if currency == "" {
		currency = "THB"
	}

	acc := &Account{
		UserID:         userID,
		Name:           req.Name,
		Type:           req.Type,
		Balance:        req.Balance,
		Currency:       currency,
		Icon:           req.Icon,
		Color:          req.Color,
		IsActive:       true,
		CreditLimit:    req.CreditLimit,
		StatementDate:  req.StatementDate,
		PaymentDueDate: req.PaymentDueDate,
		MinimumPayment: req.MinimumPayment,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := s.store.CreateAccount(ctx, acc); err != nil {
		return nil, err
	}
	return acc, nil
}

func (s *Service) GetAccounts(ctx context.Context, userID int64, isActive *bool) ([]Account, error) {
	return s.store.GetAccounts(ctx, userID, isActive)
}

func (s *Service) GetAccountByID(ctx context.Context, userID, accountID int64) (*Account, error) {
	acc, err := s.store.GetAccountByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if acc.UserID != userID {
		return nil, ErrForbidden
	}
	return acc, nil
}

func (s *Service) UpdateAccount(ctx context.Context, userID, accountID int64, req UpdateAccountRequest) (*Account, error) {
	acc, err := s.store.GetAccountByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if acc.UserID != userID {
		return nil, ErrForbidden
	}
	return s.store.UpdateAccount(ctx, accountID, req)
}

func (s *Service) DeleteAccount(ctx context.Context, userID, accountID int64) error {
	acc, err := s.store.GetAccountByID(ctx, accountID)
	if err != nil {
		return err
	}
	if acc.UserID != userID {
		return ErrForbidden
	}
	return s.store.DeactivateAccount(ctx, accountID)
}

func (s *Service) GetAccountSummary(ctx context.Context, userID, accountID int64, from, to *string) (*AccountSummary, error) {
	acc, err := s.store.GetAccountByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if acc.UserID != userID {
		return nil, ErrForbidden
	}
	return s.store.GetAccountSummary(ctx, accountID, from, to)
}

// =============================================================
// CATEGORIES
// =============================================================

func (s *Service) CreateCategory(ctx context.Context, userID int64, req CreateCategoryRequest) (*Category, error) {
	cat := &Category{
		UserID:    userID,
		Name:      req.Name,
		Type:      req.Type,
		ParentID:  req.ParentID,
		Icon:      req.Icon,
		Color:     req.Color,
		IsActive:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := s.store.CreateCategory(ctx, cat); err != nil {
		return nil, err
	}
	return cat, nil
}

func (s *Service) GetCategories(ctx context.Context, userID int64, catType string) ([]Category, error) {
	cats, err := s.store.GetCategories(ctx, userID, catType)
	if err != nil {
		return nil, err
	}
	return buildCategoryTree(cats), nil
}

func (s *Service) UpdateCategory(ctx context.Context, userID, categoryID int64, req UpdateCategoryRequest) (*Category, error) {
	cat, err := s.store.GetCategoryByID(ctx, categoryID)
	if err != nil {
		return nil, err
	}
	if cat.UserID != userID {
		return nil, ErrForbidden
	}
	return s.store.UpdateCategory(ctx, categoryID, req)
}

func (s *Service) DeleteCategory(ctx context.Context, userID, categoryID int64) error {
	cat, err := s.store.GetCategoryByID(ctx, categoryID)
	if err != nil {
		return err
	}
	if cat.UserID != userID {
		return ErrForbidden
	}
	hasChildren, err := s.store.HasChildCategories(ctx, categoryID)
	if err != nil {
		return err
	}
	if hasChildren {
		return errors.New("category has child categories, delete or reparent them first")
	}
	return s.store.DeactivateCategory(ctx, categoryID)
}

// buildCategoryTree nests children under their parents
func buildCategoryTree(flat []Category) []Category {
	byID := make(map[int64]*Category)
	var roots []Category

	// First pass: index by ID
	for i := range flat {
		flat[i].Children = nil
		byID[flat[i].ID] = &flat[i]
	}

	// Second pass: build tree
	for i := range flat {
		if flat[i].ParentID != nil {
			if parent, ok := byID[*flat[i].ParentID]; ok {
				parent.Children = append(parent.Children, flat[i])
				continue
			}
		}
		roots = append(roots, flat[i])
	}

	// Update children in roots from byID map
	for i := range roots {
		if updated, ok := byID[roots[i].ID]; ok {
			roots[i].Children = updated.Children
		}
	}

	return roots
}

// =============================================================
// TAGS
// =============================================================

func (s *Service) CreateTag(ctx context.Context, userID int64, req CreateTagRequest) (*Tag, error) {
	tag := &Tag{
		UserID:    userID,
		Name:      req.Name,
		Color:     req.Color,
		CreatedAt: time.Now(),
	}
	if err := s.store.CreateTag(ctx, tag); err != nil {
		return nil, err
	}
	return tag, nil
}

func (s *Service) GetTags(ctx context.Context, userID int64) ([]Tag, error) {
	return s.store.GetTags(ctx, userID)
}

func (s *Service) UpdateTag(ctx context.Context, userID, tagID int64, req UpdateTagRequest) (*Tag, error) {
	tag, err := s.store.GetTagByID(ctx, tagID)
	if err != nil {
		return nil, err
	}
	if tag.UserID != userID {
		return nil, ErrForbidden
	}
	return s.store.UpdateTag(ctx, tagID, req)
}

func (s *Service) DeleteTag(ctx context.Context, userID, tagID int64) error {
	tag, err := s.store.GetTagByID(ctx, tagID)
	if err != nil {
		return err
	}
	if tag.UserID != userID {
		return ErrForbidden
	}
	return s.store.DeleteTag(ctx, tagID)
}

// =============================================================
// TRANSACTIONS
// =============================================================

func (s *Service) CreateTransaction(ctx context.Context, userID int64, req CreateTransactionRequest) (*Transaction, error) {
	// Validate: transfers need destination account
	if req.Type == "transfer" && req.TransferToAccountID == nil {
		return nil, errors.New("transfer_to_account_id is required for transfers")
	}
	if req.Type == "transfer" && req.TransferToAccountID != nil && *req.TransferToAccountID == req.AccountID {
		return nil, errors.New("source and destination accounts must be different")
	}

	parsedDate, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		return nil, errors.New("invalid date format, use YYYY-MM-DD")
	}

	tx := &Transaction{
		UserID:              userID,
		AccountID:           req.AccountID,
		CategoryID:          req.CategoryID,
		Amount:              req.Amount,
		Type:                req.Type,
		TransferToAccountID: req.TransferToAccountID,
		ProjectID:           req.ProjectID,
		Note:                req.Note,
		PhotoURL:            req.PhotoURL,
		Date:                parsedDate,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	if err := s.store.CreateTransaction(ctx, tx); err != nil {
		return nil, err
	}

	// Add tags if provided
	if len(req.TagIDs) > 0 {
		if err := s.store.AddTransactionTags(ctx, tx.ID, req.TagIDs); err != nil {
			return nil, err
		}
	}

	return tx, nil
}

func (s *Service) GetTransactions(ctx context.Context, userID int64, params map[string]interface{}) ([]Transaction, int, error) {
	return s.store.GetTransactions(ctx, userID, params)
}

func (s *Service) GetTransactionByID(ctx context.Context, userID, txID int64) (*TransactionWithDetails, error) {
	tx, err := s.store.GetTransactionByID(ctx, txID)
	if err != nil {
		return nil, err
	}
	if tx.UserID != userID {
		return nil, ErrForbidden
	}

	result := &TransactionWithDetails{Transaction: *tx}

	// Fetch account brief
	acc, err := s.store.GetAccountByID(ctx, tx.AccountID)
	if err == nil {
		result.Account = &AccountBrief{ID: acc.ID, Name: acc.Name}
	}

	// Fetch category brief
	if tx.CategoryID != nil {
		cat, err := s.store.GetCategoryByID(ctx, *tx.CategoryID)
		if err == nil {
			result.Category = &CategoryBrief{ID: cat.ID, Name: cat.Name}
		}
	}

	// Fetch tags
	tags, err := s.store.GetTransactionTags(ctx, txID)
	if err == nil {
		result.Tags = tags
	}

	return result, nil
}

func (s *Service) UpdateTransaction(ctx context.Context, userID, txID int64, req UpdateTransactionRequest) (*Transaction, error) {
	tx, err := s.store.GetTransactionByID(ctx, txID)
	if err != nil {
		return nil, err
	}
	if tx.UserID != userID {
		return nil, ErrForbidden
	}

	// Handle tag replacement
	if req.TagIDs != nil {
		if err := s.store.ReplaceTransactionTags(ctx, txID, *req.TagIDs); err != nil {
			return nil, err
		}
	}

	return s.store.UpdateTransaction(ctx, txID, tx.Amount, tx.Type, tx.AccountID, req)
}

func (s *Service) DeleteTransaction(ctx context.Context, userID, txID int64) error {
	tx, err := s.store.GetTransactionByID(ctx, txID)
	if err != nil {
		return err
	}
	if tx.UserID != userID {
		return ErrForbidden
	}

	// Check if linked to shared expense
	hasShared, err := s.store.HasSharedExpense(ctx, txID)
	if err != nil {
		return err
	}
	if hasShared {
		return errors.New("cannot delete transaction linked to a shared expense")
	}

	return s.store.DeleteTransaction(ctx, tx)
}

func (s *Service) GetTransactionSummary(ctx context.Context, userID int64, from, to string, accountID *int64, groupBy string) (*TransactionSummary, error) {
	return s.store.GetTransactionSummary(ctx, userID, from, to, accountID, groupBy)
}

func (s *Service) AddTransactionTags(ctx context.Context, userID, txID int64, tagIDs []int64) ([]Tag, error) {
	tx, err := s.store.GetTransactionByID(ctx, txID)
	if err != nil {
		return nil, err
	}
	if tx.UserID != userID {
		return nil, ErrForbidden
	}
	if err := s.store.AddTransactionTags(ctx, txID, tagIDs); err != nil {
		return nil, err
	}
	return s.store.GetTransactionTags(ctx, txID)
}

func (s *Service) RemoveTransactionTag(ctx context.Context, userID, txID, tagID int64) error {
	tx, err := s.store.GetTransactionByID(ctx, txID)
	if err != nil {
		return err
	}
	if tx.UserID != userID {
		return ErrForbidden
	}
	return s.store.RemoveTransactionTag(ctx, txID, tagID)
}
