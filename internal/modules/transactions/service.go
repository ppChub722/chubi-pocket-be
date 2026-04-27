package transactions

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
)

// Service errors — mapped to HTTP codes by the handler.
var (
	ErrAccountForbidden            = errors.New("account not found or not owned")
	ErrCategoryForbidden           = errors.New("category not found or not owned")
	ErrCategoryTypeMismatch        = errors.New("category type does not match transaction type")
	ErrTransferSameAccount         = errors.New("transfer source and destination must differ")
	ErrTransferCurrencyMismatch    = errors.New("transfer source and destination must share currency")
	ErrSystemCategoryNotAllowed    = errors.New("user category required (system categories are auto-assigned)")
	ErrSplitsNotSupportedYet       = errors.New("splits are not supported in Phase 1a; ships in Phase 1b")
	ErrProjectIDNotAllowed         = errors.New("project_id is auto-managed; cannot be set on POST /v1/transactions")
	ErrTransferToAccountRequired   = errors.New("transfer_to_account_id is required for type=transfer")
	ErrTransferFieldsOnNonTransfer = errors.New("transfer_to_account_id is only valid for type=transfer")
	ErrAmountInvalid               = errors.New("amount must be > 0")
	ErrCategoryRequiredForTransfer = errors.New("transfer category is auto-set; do not pass category_id")
	ErrTransferEditCurrency        = errors.New("cannot edit currency on a transfer; delete and recreate")
	ErrTransferCategoryEdit        = errors.New("cannot change category on a transfer; delete and recreate")
)

type Service struct {
	store *Store
	cats  *categories.Service
}

func NewService(s *Store, cats *categories.Service) *Service {
	return &Service{store: s, cats: cats}
}

// CountByCategory exposes the underlying count to categories.Service via the
// TransactionCounter func wired in main.go.
func (s *Service) CountByCategory(ctx context.Context, categoryID uuid.UUID) (int, error) {
	return s.store.CountByCategory(ctx, categoryID)
}

// CreateSystemInTx inserts a transaction backed by a system category (Opening
// Balance / Adjustment / Transfer) inside `tx`. Used by the accounts module
// for opening-balance auto-tx and adjust-balance flows that the public
// CreateInTx wouldn't accept (it rejects system categories).
//
// Returns the created row + the account's new balance after the delta.
func (s *Service) CreateSystemInTx(
	ctx context.Context, tx pgx.Tx, userID uuid.UUID,
	accountID uuid.UUID, kind categories.SystemKind,
	amount float64, date string, note *string,
) (*Transaction, float64, error) {
	if amount <= 0 {
		return nil, 0, ErrAmountInvalid
	}
	if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{accountID}); err != nil {
		return nil, 0, err
	}
	if _, _, err := s.store.GetAccountBalanceTx(ctx, tx, userID, accountID); err != nil {
		return nil, 0, err
	}

	cat, err := s.cats.SystemFor(ctx, userID, kind)
	if err != nil {
		return nil, 0, fmt.Errorf("lookup system category %s: %w", kind, err)
	}

	txType := TypeIncome
	if cat.Type == "expense" {
		txType = TypeExpense
	}

	row := &Transaction{
		UserID:     userID,
		AccountID:  accountID,
		Type:       txType,
		Amount:     amount,
		CategoryID: &cat.ID,
		Date:       date,
		Note:       note,
	}
	created, err := s.store.InsertRowTx(ctx, tx, row)
	if err != nil {
		return nil, 0, err
	}
	delta := signedDelta(txType, amount, false)
	newBalance, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, accountID, delta)
	if err != nil {
		return nil, 0, err
	}
	return created, newBalance, nil
}

// --- Read ---

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*TransactionDetail, error) {
	t, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return s.hydrate(ctx, userID, t)
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, f ListFilter) (*ListResponse, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PerPage <= 0 {
		f.PerPage = 20
	}
	if f.PerPage > 100 {
		f.PerPage = 100
	}

	rows, total, err := s.store.List(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	totalPages := (total + f.PerPage - 1) / f.PerPage
	if totalPages == 0 {
		totalPages = 1
	}
	return &ListResponse{
		Data: rows,
		Pagination: Pagination{
			Page:       f.Page,
			PerPage:    f.PerPage,
			Total:      total,
			TotalPages: totalPages,
		},
	}, nil
}

// hydrate fills the embedded account + category names for a single Transaction.
func (s *Service) hydrate(ctx context.Context, userID uuid.UUID, t *Transaction) (*TransactionDetail, error) {
	d := &TransactionDetail{Transaction: *t}
	// Account ref — bare query rather than importing accounts module.
	var accName string
	err := s.store.db.QueryRow(ctx,
		`SELECT name FROM accounts WHERE id = $1 AND user_id = $2`,
		t.AccountID, userID).Scan(&accName)
	if err == nil {
		d.Account = &EmbeddedRef{ID: t.AccountID, Name: accName}
	}
	if t.CategoryID != nil {
		c, err := s.cats.GetCategoryRow(ctx, userID, *t.CategoryID)
		if err == nil {
			d.Category = &EmbeddedRef{ID: c.ID, Name: c.Name}
		}
	}
	return d, nil
}

// --- Create (the atomic balance flow) ---

// Create opens a tx, calls CreateInTx, commits. Used by HTTP handler.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, req CreateRequest) (any, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	res, err := s.CreateInTx(ctx, tx, userID, req)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	return res, nil
}

// CreateInTx writes the transaction(s) and balance delta(s) inside `tx`.
// Used by both Service.Create AND accounts.Service for the opening-balance
// and adjust-balance flows that compose the account write with the
// transaction write in one atomic tx.
//
// Returns:
//   - For expense/income: (*TransactionDetail, nil)
//   - For transfer:        (*TransferResponse, nil)
func (s *Service) CreateInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, req CreateRequest) (any, error) {
	if err := s.validateCreateCommon(req); err != nil {
		return nil, err
	}

	switch req.Type {
	case TypeExpense, TypeIncome:
		return s.createSingleInTx(ctx, tx, userID, req)
	case TypeTransfer:
		return s.createTransferInTx(ctx, tx, userID, req)
	default:
		return nil, ErrAmountInvalid // unreachable; binding catches this
	}
}

func (s *Service) validateCreateCommon(req CreateRequest) error {
	if len(req.Splits) > 0 || req.MyShare != nil {
		return ErrSplitsNotSupportedYet
	}
	if req.ProjectID != nil {
		return ErrProjectIDNotAllowed
	}
	if req.Amount <= 0 {
		return ErrAmountInvalid
	}
	if req.Type == TypeTransfer {
		if req.TransferToAccountID == nil {
			return ErrTransferToAccountRequired
		}
		if *req.TransferToAccountID == req.AccountID {
			return ErrTransferSameAccount
		}
		if req.CategoryID != nil {
			return ErrCategoryRequiredForTransfer
		}
	} else {
		if req.TransferToAccountID != nil {
			return ErrTransferFieldsOnNonTransfer
		}
	}
	return nil
}

func (s *Service) createSingleInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, req CreateRequest) (*TransactionDetail, error) {
	// Lock the account (single account → trivial sort).
	if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{req.AccountID}); err != nil {
		return nil, err
	}
	balance, _, err := s.store.GetAccountBalanceTx(ctx, tx, userID, req.AccountID)
	if err != nil {
		return nil, err
	}

	// Validate category if provided. Allowed: nil, or user category whose
	// type matches the transaction type. System cats not allowed for
	// expense/income (auto-assignment is for transfers / opening / adjust
	// only — those flows go through their own paths).
	var categoryID *uuid.UUID
	if req.CategoryID != nil {
		c, err := s.cats.GetCategoryRow(ctx, userID, *req.CategoryID)
		if err != nil {
			return nil, ErrCategoryForbidden
		}
		if c.IsSystem {
			return nil, ErrSystemCategoryNotAllowed
		}
		if c.Type != string(req.Type) {
			return nil, ErrCategoryTypeMismatch
		}
		categoryID = &c.ID
	}

	delta := signedDelta(req.Type, req.Amount, false)

	row := &Transaction{
		UserID:     userID,
		AccountID:  req.AccountID,
		Type:       req.Type,
		Amount:     req.Amount,
		CategoryID: categoryID,
		Date:       req.Date,
		Note:       req.Note,
	}
	created, err := s.store.InsertRowTx(ctx, tx, row)
	if err != nil {
		return nil, err
	}
	newBalance, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, req.AccountID, delta)
	if err != nil {
		return nil, err
	}
	_ = balance // currentBalance not needed beyond ownership confirmation

	d := &TransactionDetail{Transaction: *created, AccountBalanceAfter: &newBalance}
	if categoryID != nil {
		c, _ := s.cats.GetCategoryRow(ctx, userID, *categoryID)
		if c != nil {
			d.Category = &EmbeddedRef{ID: c.ID, Name: c.Name}
		}
	}
	return d, nil
}

func (s *Service) createTransferInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, req CreateRequest) (*TransferResponse, error) {
	dst := *req.TransferToAccountID
	src := req.AccountID

	// Lock both accounts in lowest-UUID-first order (deadlock prevention).
	lockIDs := []uuid.UUID{src, dst}
	sort.Slice(lockIDs, func(i, j int) bool { return uuidLess(lockIDs[i], lockIDs[j]) })
	if err := s.store.LockAccountsForUpdate(ctx, tx, lockIDs); err != nil {
		return nil, err
	}

	_, srcCurrency, err := s.store.GetAccountBalanceTx(ctx, tx, userID, src)
	if err != nil {
		return nil, err
	}
	_, dstCurrency, err := s.store.GetAccountBalanceTx(ctx, tx, userID, dst)
	if err != nil {
		return nil, err
	}
	if srcCurrency != dstCurrency {
		return nil, ErrTransferCurrencyMismatch
	}

	transferOutCat, err := s.cats.SystemFor(ctx, userID, categories.SystemTransferOut)
	if err != nil {
		return nil, fmt.Errorf("lookup TRANSFER_OUT: %w", err)
	}
	transferInCat, err := s.cats.SystemFor(ctx, userID, categories.SystemTransferIn)
	if err != nil {
		return nil, fmt.Errorf("lookup TRANSFER_IN: %w", err)
	}

	groupID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}

	// OUT row → debits source
	outRow, err := s.store.InsertRowTx(ctx, tx, &Transaction{
		UserID:          userID,
		AccountID:       src,
		Type:            TypeTransfer,
		Amount:          req.Amount,
		CategoryID:      &transferOutCat.ID,
		Date:            req.Date,
		Note:            req.Note,
		TransferGroupID: &groupID,
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, src, -req.Amount); err != nil {
		return nil, err
	}

	// IN row → credits destination
	inRow, err := s.store.InsertRowTx(ctx, tx, &Transaction{
		UserID:          userID,
		AccountID:       dst,
		Type:            TypeTransfer,
		Amount:          req.Amount,
		CategoryID:      &transferInCat.ID,
		Date:            req.Date,
		Note:            req.Note,
		TransferGroupID: &groupID,
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, dst, req.Amount); err != nil {
		return nil, err
	}

	return &TransferResponse{
		TransferGroupID: groupID,
		Rows: []TransferRow{
			{ID: outRow.ID, AccountID: src, Category: transferOutCat.Name, Amount: req.Amount},
			{ID: inRow.ID, AccountID: dst, Category: transferInCat.Name, Amount: req.Amount},
		},
	}, nil
}

// --- Update ---

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*TransactionDetail, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	if current.Type == TypeTransfer {
		updated, err := s.updateTransferInTx(ctx, tx, userID, current, req)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
		return s.hydrate(ctx, userID, updated)
	}

	updated, err := s.updateSingleInTx(ctx, tx, userID, current, req)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.hydrate(ctx, userID, updated)
}

func (s *Service) updateSingleInTx(
	ctx context.Context, tx pgx.Tx, userID uuid.UUID,
	current *Transaction, req UpdateRequest,
) (*Transaction, error) {
	// Lock the account
	if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{current.AccountID}); err != nil {
		return nil, err
	}

	// Validate category if changing
	newCatID, catChange := req.CategoryIDChange()
	if catChange && newCatID != nil {
		c, err := s.cats.GetCategoryRow(ctx, userID, *newCatID)
		if err != nil {
			return nil, ErrCategoryForbidden
		}
		if c.IsSystem {
			return nil, ErrSystemCategoryNotAllowed
		}
		if c.Type != string(current.Type) {
			return nil, ErrCategoryTypeMismatch
		}
	}

	// Compute amount delta if changing
	if req.Amount != nil && *req.Amount != current.Amount {
		oldDelta := signedDelta(current.Type, current.Amount, false)
		newDelta := signedDelta(current.Type, *req.Amount, false)
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, current.AccountID, newDelta-oldDelta); err != nil {
			return nil, err
		}
	}

	noteVal, noteChanged := req.NoteChange()
	updated, err := s.store.UpdateRowTx(ctx, tx, userID, current.ID,
		req.Amount, req.Date,
		newCatID, catChange,
		noteVal, noteChanged)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Service) updateTransferInTx(
	ctx context.Context, tx pgx.Tx, userID uuid.UUID,
	current *Transaction, req UpdateRequest,
) (*Transaction, error) {
	if _, catChange := req.CategoryIDChange(); catChange {
		return nil, ErrTransferCategoryEdit
	}
	if current.TransferGroupID == nil {
		return nil, fmt.Errorf("transfer row has no transfer_group_id")
	}

	// Fetch both rows
	pair, err := s.store.GetByGroupID(ctx, userID, *current.TransferGroupID)
	if err != nil {
		return nil, err
	}
	if len(pair) != 2 {
		return nil, fmt.Errorf("transfer pair has %d rows; expected 2", len(pair))
	}

	// Identify OUT vs IN by category
	outCat, err := s.cats.SystemFor(ctx, userID, categories.SystemTransferOut)
	if err != nil {
		return nil, err
	}
	var outRow, inRow *Transaction
	for i := range pair {
		if pair[i].CategoryID != nil && *pair[i].CategoryID == outCat.ID {
			outRow = &pair[i]
		} else {
			inRow = &pair[i]
		}
	}
	if outRow == nil || inRow == nil {
		return nil, fmt.Errorf("could not identify OUT/IN rows of transfer")
	}

	// Lock both accounts (lowest UUID first)
	lockIDs := []uuid.UUID{outRow.AccountID, inRow.AccountID}
	sort.Slice(lockIDs, func(i, j int) bool { return uuidLess(lockIDs[i], lockIDs[j]) })
	if err := s.store.LockAccountsForUpdate(ctx, tx, lockIDs); err != nil {
		return nil, err
	}

	// Amount change → reverse old, apply new on both rows
	if req.Amount != nil && *req.Amount != current.Amount {
		old := current.Amount
		new := *req.Amount
		// OUT side: was -old, now -new → delta = old - new (positive when shrinking)
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, outRow.AccountID, old-new); err != nil {
			return nil, err
		}
		// IN side: was +old, now +new → delta = new - old
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, inRow.AccountID, new-old); err != nil {
			return nil, err
		}
	}

	noteVal, noteChanged := req.NoteChange()

	// Update both rows
	updatedOut, err := s.store.UpdateRowTx(ctx, tx, userID, outRow.ID,
		req.Amount, req.Date, nil, false, noteVal, noteChanged)
	if err != nil {
		return nil, err
	}
	updatedIn, err := s.store.UpdateRowTx(ctx, tx, userID, inRow.ID,
		req.Amount, req.Date, nil, false, noteVal, noteChanged)
	if err != nil {
		return nil, err
	}

	// Return whichever one the caller asked about
	if current.ID == updatedOut.ID {
		return updatedOut, nil
	}
	return updatedIn, nil
}

// --- Delete ---

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return err
	}

	if current.Type == TypeTransfer {
		if err := s.deleteTransferInTx(ctx, tx, userID, current); err != nil {
			return err
		}
	} else {
		if err := s.deleteSingleInTx(ctx, tx, userID, current); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) deleteSingleInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, current *Transaction) error {
	if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{current.AccountID}); err != nil {
		return err
	}
	delta := signedDelta(current.Type, current.Amount, false)
	if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, current.AccountID, -delta); err != nil {
		return err
	}
	return s.store.DeleteRowTx(ctx, tx, userID, current.ID)
}

func (s *Service) deleteTransferInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, current *Transaction) error {
	if current.TransferGroupID == nil {
		return fmt.Errorf("transfer row missing transfer_group_id")
	}
	pair, err := s.store.GetByGroupID(ctx, userID, *current.TransferGroupID)
	if err != nil {
		return err
	}
	if len(pair) != 2 {
		return fmt.Errorf("transfer pair has %d rows; expected 2", len(pair))
	}

	outCat, err := s.cats.SystemFor(ctx, userID, categories.SystemTransferOut)
	if err != nil {
		return err
	}
	var outRow, inRow *Transaction
	for i := range pair {
		if pair[i].CategoryID != nil && *pair[i].CategoryID == outCat.ID {
			outRow = &pair[i]
		} else {
			inRow = &pair[i]
		}
	}
	if outRow == nil || inRow == nil {
		return fmt.Errorf("could not identify OUT/IN rows")
	}

	lockIDs := []uuid.UUID{outRow.AccountID, inRow.AccountID}
	sort.Slice(lockIDs, func(i, j int) bool { return uuidLess(lockIDs[i], lockIDs[j]) })
	if err := s.store.LockAccountsForUpdate(ctx, tx, lockIDs); err != nil {
		return err
	}

	// Reverse balances
	if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, outRow.AccountID, current.Amount); err != nil {
		return err
	}
	if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, inRow.AccountID, -current.Amount); err != nil {
		return err
	}

	// Delete both rows
	if err := s.store.DeleteRowTx(ctx, tx, userID, outRow.ID); err != nil {
		return err
	}
	if err := s.store.DeleteRowTx(ctx, tx, userID, inRow.ID); err != nil {
		return err
	}
	return nil
}

// --- Summary ---

func (s *Service) Summary(ctx context.Context, userID uuid.UUID, req SummaryRequest) (*SummaryResponse, error) {
	whereClauses := []string{"user_id = $1", "date >= $2::date", "date <= $3::date"}
	args := []any{userID, req.From, req.To}

	if req.AccountID != nil {
		args = append(args, *req.AccountID)
		whereClauses = append(whereClauses, fmt.Sprintf("account_id = $%d", len(args)))
	}
	if req.CategoryID != nil {
		args = append(args, *req.CategoryID)
		whereClauses = append(whereClauses, fmt.Sprintf("category_id = $%d", len(args)))
	}
	where := joinAnd(whereClauses)

	var (
		totalIncome  float64
		totalExpense float64
		count        int
	)
	q := `SELECT
		COALESCE(SUM(amount) FILTER (WHERE type = 'income'),  0),
		COALESCE(SUM(amount) FILTER (WHERE type = 'expense'), 0),
		COUNT(*) FILTER (WHERE type IN ('income', 'expense'))
		FROM transactions WHERE ` + where
	if err := s.store.db.QueryRow(ctx, q, args...).Scan(&totalIncome, &totalExpense, &count); err != nil {
		return nil, fmt.Errorf("summary: %w", err)
	}

	resp := &SummaryResponse{
		From:             req.From,
		To:               req.To,
		Currency:         "THB", // Phase 1 single-currency assumption
		TotalIncome:      totalIncome,
		TotalExpense:     totalExpense,
		Net:              totalIncome - totalExpense,
		TransactionCount: count,
	}

	switch req.GroupBy {
	case "category":
		resp.Groups, _ = s.summaryByCategory(ctx, where, args)
	case "account":
		resp.Groups, _ = s.summaryByAccount(ctx, where, args)
	case "day", "week", "month":
		resp.Groups, _ = s.summaryByDate(ctx, where, args, req.GroupBy)
	}

	return resp, nil
}

func (s *Service) summaryByCategory(ctx context.Context, where string, args []any) ([]SummaryGroup, error) {
	q := `SELECT COALESCE(c.id::text, ''), COALESCE(c.name, '(uncategorized)'),
		COALESCE(SUM(t.amount), 0), COUNT(*)
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE ` + where + ` AND t.type IN ('income','expense')
		GROUP BY c.id, c.name
		ORDER BY SUM(t.amount) DESC`
	return s.scanSummary(ctx, q, args)
}

func (s *Service) summaryByAccount(ctx context.Context, where string, args []any) ([]SummaryGroup, error) {
	q := `SELECT a.id::text, a.name,
		COALESCE(SUM(t.amount), 0), COUNT(*)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id
		WHERE ` + where + ` AND t.type IN ('income','expense')
		GROUP BY a.id, a.name
		ORDER BY SUM(t.amount) DESC`
	return s.scanSummary(ctx, q, args)
}

func (s *Service) summaryByDate(ctx context.Context, where string, args []any, period string) ([]SummaryGroup, error) {
	trunc := "day"
	switch period {
	case "week":
		trunc = "week"
	case "month":
		trunc = "month"
	}
	q := fmt.Sprintf(`SELECT
		to_char(date_trunc('%s', t.date), 'YYYY-MM-DD'),
		'',
		COALESCE(SUM(t.amount), 0),
		COUNT(*)
		FROM transactions t
		WHERE %s AND t.type IN ('income','expense')
		GROUP BY date_trunc('%s', t.date)
		ORDER BY 1 ASC`, trunc, where, trunc)
	return s.scanSummary(ctx, q, args)
}

func (s *Service) scanSummary(ctx context.Context, q string, args []any) ([]SummaryGroup, error) {
	rows, err := s.store.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("summary group: %w", err)
	}
	defer rows.Close()
	out := make([]SummaryGroup, 0)
	for rows.Next() {
		var g SummaryGroup
		if err := rows.Scan(&g.Key, &g.Name, &g.Total, &g.Count); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// --- Internal ---

func uuidLess(a, b uuid.UUID) bool {
	for i := 0; i < 16; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}
