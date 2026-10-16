package transactions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Service errors — mapped to HTTP codes by the handler.
var (
	ErrAccountForbidden            = errors.New("account not found or not owned")
	ErrAccountArchived             = errors.New("wallet is archived: restore it first")
	ErrCategoryForbidden           = errors.New("category not found or not owned")
	ErrCategoryTypeMismatch        = errors.New("category type does not match transaction type")
	ErrTransferSameAccount         = errors.New("transfer source and destination must differ")
	ErrTransferCurrencyMismatch    = errors.New("transfer source and destination must share currency")
	ErrSystemCategoryNotAllowed    = errors.New("user category required (system categories are auto-assigned)")
	ErrSplitsNotSupportedYet       = errors.New("splits are not supported in Phase 1a; ships in Phase 1b")
	ErrSplitsOnTransfer            = errors.New("transfer transactions cannot have splits")
	ErrProjectIDNotAllowed         = errors.New("project_id is auto-managed; cannot be set on POST /v1/transactions")
	ErrTransferToAccountRequired   = errors.New("transfer_to_account_id is required for type=transfer")
	ErrTransferFieldsOnNonTransfer = errors.New("transfer_to_account_id is only valid for type=transfer")
	ErrAmountInvalid               = errors.New("amount must be > 0")
	ErrSplitContactNotFound        = errors.New("split contact not found or not owned")
	// PUT /transactions/:id/splits (owner 2026-10-10).
	ErrSplitsAuthorOnly            = errors.New("only the transaction's author can change its splits")
	ErrSplitsExceedAmount          = errors.New("splits add up to more than the transaction amount")
	ErrSplitUnknownDebt            = errors.New("debt_id is not one of this transaction's splits (or is listed twice)")
	ErrSplitPersonRequired         = errors.New("person_name is required for a new split")
	ErrSplitsExceedShare           = errors.New("splits add up to more than your share of this event bill")
	ErrCategoryRequiredForTransfer = errors.New("transfer category is auto-set; do not pass category_id")
	ErrTransferRequiresAccount     = errors.New("account_id is required for type=transfer")
	ErrTransferEditCurrency        = errors.New("cannot edit currency on a transfer; delete and recreate")
	ErrTransferCategoryEdit        = errors.New("cannot change category on a transfer; delete and recreate")
	// tag_ids / PUT /:id/tags: every id must be one of the caller's tags.
	ErrTagNotFound = errors.New("tag not found")
	// System-category rows (Opening Balance, Adjustment) are bookkeeping
	// reflections of account-level operations and aren't user-mutable
	// directly — see spec §03/§3.4 (account edit), §03/§2.5 (adjust),
	// §04/§4.16 (transfer pair). Transfer rows are NOT blocked here:
	// they have their own cascade path in updateTransferInTx + Delete.
	ErrSystemTransactionImmutable = errors.New("transactions in a system category cannot be edited or deleted directly; use the account-level operation that created them")
	// Shared wallets (spec §14/2). Rule 2: only the row's author may
	// change category_id (their taxonomy). Rule 3: leaving locks your
	// rows — an ex-member can no longer edit/delete their old rows on
	// the wallet they left.
	ErrCategoryAuthorOnly = errors.New("only the row's author may change its category")
	ErrRowLocked          = errors.New("membership on this account has ended; the row is read-only for you")
)

// Cross-module hooks. Wired post-construction in main.go to break the
// transactions ↔ personal_debts cycle. Each hook is optional in the
// sense that "feature off" = nil callback; the service rejects requests
// that need the missing hook with a clear error rather than silently no-op.
type (
	// DebtsCreator inserts one personal_debts row per split entry on the
	// splitter's side AND one mirror row on each linked partner's side.
	// Direction follows parentType ('expense' | 'income'): on an expense
	// the splitter fronted money → counterparties owe them
	// ('owed_to_me', mirror 'i_owe'); on an income the splitter received
	// money that partly belongs to others → they owe the counterparties
	// ('i_owe', mirror 'owed_to_me'). Called inside Create's tx after the
	// parent transaction row lands. Implemented by
	// personal_debts.Service.CreateForTransactionTx.
	DebtsCreator func(ctx context.Context, tx pgx.Tx, parentTxID, userID uuid.UUID, parentType, parentCurrency string, parentDescription *string, splits []SplitInput) error

	// SplitsEditor replaces parentTxID's split debts with `edits` (PUT
	// /transactions/:id/splits) — add, re-amount, remove, and tell linked
	// partners. Inside the caller's tx; wired from personal_debts in main.
	SplitsEditor func(ctx context.Context, tx pgx.Tx, parentTxID, userID uuid.UUID, parentType, parentCurrency string, parentDescription *string, edits []SplitEdit) error
)

type Service struct {
	store        *Store
	cats         *categories.Service
	debtsCreator DebtsCreator
	splitsEditor SplitsEditor
	debtLinks    DebtLinks
	eventBills   EventBillSyncer
	billDeleter  EventBillDeleter
	recurring    RecurringLinks
}

func NewService(s *Store, cats *categories.Service) *Service {
	return &Service{store: s, cats: cats}
}

func (s *Service) WithDebtsCreator(fn DebtsCreator) { s.debtsCreator = fn }
func (s *Service) WithSplitsEditor(fn SplitsEditor) { s.splitsEditor = fn }

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
	amount float64, date string, description, note *string,
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

	accID := accountID // local copy for pointer
	row := &Transaction{
		UserID:      userID,
		AccountID:   &accID,
		Type:        txType,
		Amount:      amount,
		CategoryID:  &cat.ID,
		Date:        date,
		Description: description,
		Note:        note,
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

// Get — GET /transactions/:id: the page-ready row with its splits.
func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*TransactionDetail, error) {
	t, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return detailOf(ctx, s.store.db, userID, t)
}

// List — GET /transactions: a page of page-ready rows (splits null) plus
// totals over the whole filtered set.
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

	rows, total, totals, err := s.store.List(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	data, err := buildDetails(ctx, s.store.db, userID, rows, false)
	if err != nil {
		return nil, err
	}
	return &ListResponse{
		Data:       data,
		Pagination: shared.NewPage(f.Page, f.PerPage, total),
		Totals:     totals,
	}, nil
}

// buildTransferResponse resolves both rows of a transfer (OUT first, IN
// second) through q (the pool, or the write's tx before commit).
func buildTransferResponse(
	ctx context.Context, q shared.Querier, userID, groupID uuid.UUID, outRow, inRow *Transaction,
) (*TransferResponse, error) {
	rows, err := buildDetails(ctx, q, userID, []Transaction{*outRow, *inRow}, true)
	if err != nil {
		return nil, err
	}
	return &TransferResponse{TransferGroupID: groupID, Rows: rows}, nil
}

// --- Create (the atomic balance flow) ---

// Create opens a tx, calls CreateInTx, commits. Returns what CreateInTx
// returns (the rows are already in their final state).
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

// CreateWrite — POST /transactions: Create, answered as a WriteResult (the
// row(s) + the post-commit balances of the wallets they sit on).
func (s *Service) CreateWrite(ctx context.Context, userID uuid.UUID, req CreateRequest) (*WriteResult, error) {
	// A date after today: a one-off upcoming item, not a transaction.
	if future, err := s.isFuture(ctx, userID, req.Date); err != nil {
		return nil, err
	} else if future {
		if err := s.validateCreateCommon(req); err != nil {
			return nil, err
		}
		return s.createUpcoming(ctx, userID, req)
	}
	res, err := s.Create(ctx, userID, req)
	if err != nil {
		return nil, err
	}
	var rows []TransactionDetail
	switch r := res.(type) {
	case *TransactionDetail:
		rows = []TransactionDetail{*r}
	case *TransferResponse:
		rows = r.Rows
	default:
		return nil, fmt.Errorf("create: unexpected result %T", res)
	}
	var touched []uuid.UUID
	for i := range rows {
		if rows[i].AccountID != nil {
			touched = append(touched, *rows[i].AccountID)
		}
	}
	return s.writeResult(ctx, rows, touched)
}

func (s *Service) writeResult(ctx context.Context, rows []TransactionDetail, touched []uuid.UUID) (*WriteResult, error) {
	balances, err := shared.LoadWalletBalances(ctx, s.store.db, touched)
	if err != nil {
		return nil, err
	}
	return &WriteResult{Rows: rows, AccountBalances: balances}, nil
}

// CreateInTx writes the transaction(s), their tags and balance delta(s)
// inside `tx`. Used by both Service.Create AND accounts.Service for the
// opening-balance and adjust-balance flows that compose the account write
// with the transaction write in one atomic tx.
//
// Returns (rows resolved inside tx, final state):
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
		// Splits create personal_debts rows in the same tx. Reject upfront
		// when the hook isn't wired so we don't silently lose split data.
		if s.debtsCreator == nil {
			return ErrSplitsNotSupportedYet
		}
		if req.Type == TypeTransfer {
			return ErrSplitsOnTransfer
		}
	}
	if req.ProjectID != nil {
		// Not accepted: the project resolve flow uses
		// source_project_transaction_id and the BE derives the project.
		return ErrProjectIDNotAllowed
	}
	if req.SourceProjectTransactionID != nil && req.Type == TypeTransfer {
		return ErrSplitsOnTransfer
	}
	if req.Amount <= 0 {
		return ErrAmountInvalid
	}
	if req.Type == TypeTransfer {
		if req.AccountID == nil {
			return ErrTransferRequiresAccount
		}
		if req.TransferToAccountID == nil {
			return ErrTransferToAccountRequired
		}
		if *req.TransferToAccountID == *req.AccountID {
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
	var currency string
	if req.AccountID != nil {
		if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*req.AccountID}); err != nil {
			return nil, err
		}
		_, cur, err := s.store.GetAccountBalanceTx(ctx, tx, userID, *req.AccountID)
		if err != nil {
			return nil, err
		}
		currency = cur
	} else {
		// No wallet: split debts take the user's currency (same fallback
		// as EditSplits) — never ''.
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE((SELECT currency FROM users WHERE id = $1), 'THB')`,
			userID).Scan(&currency); err != nil {
			return nil, fmt.Errorf("user currency: %w", err)
		}
	}

	// Validate category if provided. System cats not allowed for
	// expense/income — those flows go through their own paths.
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

	// Project resolve flow: when the caller passes
	// source_project_transaction_id, derive project_id from it and stamp
	// both columns on the personal-mirror row.
	var projectID *uuid.UUID
	if req.SourceProjectTransactionID != nil {
		pid, err := s.store.LookupSourcePTTx(ctx, tx, *req.SourceProjectTransactionID, userID)
		if err != nil {
			return nil, err
		}
		projectID = &pid
	}

	row := &Transaction{
		UserID:                     userID,
		AccountID:                  req.AccountID,
		Type:                       req.Type,
		Amount:                     req.Amount,
		CategoryID:                 categoryID,
		Date:                       req.Date,
		Description:                req.Description,
		Note:                       req.Note,
		ProjectID:                  projectID,
		SourceProjectTransactionID: req.SourceProjectTransactionID,
	}
	created, err := s.store.InsertRowTx(ctx, tx, row)
	if err != nil {
		return nil, err
	}

	if req.AccountID != nil {
		delta := signedDelta(req.Type, req.Amount, false)
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *req.AccountID, delta); err != nil {
			return nil, err
		}
	}

	if len(req.Splits) > 0 {
		if err := s.debtsCreator(ctx, tx, created.ID, userID, string(created.Type), currency, created.Description, req.Splits); err != nil {
			return nil, err
		}
	}
	if err := s.store.AttachTagsTx(ctx, tx, userID, created.ID, req.TagIDs); err != nil {
		return nil, err
	}
	return detailOf(ctx, tx, userID, created)
}

func (s *Service) createTransferInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, req CreateRequest) (*TransferResponse, error) {
	src := *req.AccountID
	dst := *req.TransferToAccountID

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

	srcID := src
	dstID := dst

	// OUT row → debits source
	outRow, err := s.store.InsertRowTx(ctx, tx, &Transaction{
		UserID:          userID,
		AccountID:       &srcID,
		Type:            TypeTransfer,
		Amount:          req.Amount,
		CategoryID:      &transferOutCat.ID,
		Date:            req.Date,
		Description:     req.Description,
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
		AccountID:       &dstID,
		Type:            TypeTransfer,
		Amount:          req.Amount,
		CategoryID:      &transferInCat.ID,
		Date:            req.Date,
		Description:     req.Description,
		Note:            req.Note,
		TransferGroupID: &groupID,
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, dst, req.Amount); err != nil {
		return nil, err
	}

	// A transfer's tags go on its OUT row (as a submitted draft's do).
	if err := s.store.AttachTagsTx(ctx, tx, userID, outRow.ID, req.TagIDs); err != nil {
		return nil, err
	}
	return buildTransferResponse(ctx, tx, userID, groupID, outRow, inRow)
}

// --- Update ---

// UpdateInTx — a non-transfer row edit inside the caller's transaction,
// under the same rules as Update (row authz, system rows read-only).
// Used by the project "update to match" auto-action (contract §5).
func (s *Service) UpdateInTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, req UpdateRequest) error {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return err
	}
	if current.Type == TypeTransfer {
		return ErrSplitsOnTransfer
	}
	if err := s.authorizeRowMutation(ctx, userID, current, req); err != nil {
		return err
	}
	if err := s.checkNotSystemRow(ctx, current); err != nil {
		return err
	}
	if _, err = s.updateSingleInTx(ctx, tx, userID, current, req); err != nil {
		return err
	}
	return s.replaceTagsIfSent(ctx, tx, userID, id, req)
}

// updated — what an edit wrote: the final row(s) (a transfer's OUT row
// first) and every wallet it touched (before and after a move).
type updated struct {
	rows    []Transaction
	group   *uuid.UUID // transfers
	touched []uuid.UUID
	// The debt entry the edited row is the wallet side of.
	debtEntry *uuid.UUID
}

// Update returns either *TransactionDetail (single row) or
// *TransferResponse (transfer pair) — for Go callers; the HTTP handler uses
// UpdateWrite.
func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (any, error) {
	u, err := s.update(ctx, userID, id, req)
	if err != nil {
		return nil, err
	}
	if u.group != nil {
		return buildTransferResponse(ctx, s.store.db, userID, *u.group, &u.rows[0], &u.rows[1])
	}
	return detailOf(ctx, s.store.db, userID, &u.rows[0])
}

// UpdateWrite — PUT /transactions/:id: the edited row(s) and the
// post-commit balances of every wallet the edit touched.
func (s *Service) UpdateWrite(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*WriteResult, error) {
	// Moved to a date after today: the row leaves transactions.
	if req.Date != nil {
		if future, err := s.isFuture(ctx, userID, *req.Date); err != nil {
			return nil, err
		} else if future {
			current, err := s.store.GetByID(ctx, userID, id)
			if err != nil {
				return nil, err
			}
			return s.moveToUpcoming(ctx, userID, current, req)
		}
	}
	u, err := s.update(ctx, userID, id, req)
	if err != nil {
		return nil, err
	}
	rows, err := buildDetails(ctx, s.store.db, userID, u.rows, true)
	if err != nil {
		return nil, err
	}
	out, err := s.writeResult(ctx, rows, u.touched)
	if err != nil {
		return nil, err
	}
	if u.debtEntry != nil && s.debtLinks != nil {
		if out.PersonBalance, err = s.debtLinks.PersonBalanceByEntry(ctx, userID, *u.debtEntry); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*updated, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	var out updated
	if current.Type == TypeTransfer {
		outRow, inRow, touched, err := s.updateTransferInTx(ctx, tx, userID, current, req)
		if err != nil {
			return nil, err
		}
		out = updated{rows: []Transaction{*outRow, *inRow}, group: current.TransferGroupID, touched: touched}
	} else {
		// Shared-wallet authz (spec §14/2): active members may edit any
		// member's rows EXCEPT category_id (author-only); ex-members can no
		// longer touch their own old rows on the wallet they left.
		if err := s.authorizeRowMutation(ctx, userID, current, req); err != nil {
			return nil, err
		}
		if current.SourcePersonalDebtID != nil {
			// A debt entry's wallet side: one record with the entry.
			if err := checkDebtTxEdit(userID, current, req, false); err != nil {
				return nil, err
			}
		} else if err := s.checkNotSystemRow(ctx, current); err != nil {
			// Non-transfer with a system category (Opening Balance /
			// Adjustment) is read-only — the user can't directly edit the
			// auto-created bookkeeping row. They mutate the account-level
			// state instead (account edit / adjust-balance).
			return nil, err
		}
		bill, err := linkedBillTx(ctx, tx, current)
		if err != nil {
			return nil, err
		}
		row, err := s.updateSingleInTx(ctx, tx, userID, current, req)
		if err != nil {
			return nil, err
		}
		// An event bill made from this row shares its money.
		if bill != nil && req.AlsoUpdateLink && s.eventBills != nil && (row.Amount != current.Amount || row.Date != current.Date) {
			if err := s.eventBills(ctx, tx, userID, *bill, row.Amount, row.Date); err != nil {
				return nil, err
			}
		}
		// A recurring item linked to this row, when asked.
		if req.AlsoUpdateLink && s.recurring != nil {
			tagIDs, tagsSet := req.TagIDsChange()
			if err := s.recurring.TxEditedTx(ctx, tx, userID, row, tagIDs, tagsSet); err != nil {
				return nil, err
			}
		}
		if row.SourcePersonalDebtID != nil && s.debtLinks != nil {
			if err := s.debtLinks.TxEditedTx(ctx, tx, userID, *row.SourcePersonalDebtID, row); err != nil {
				return nil, err
			}
		}
		out = updated{rows: []Transaction{*row}, touched: nonNilIDs(current.AccountID, row.AccountID), debtEntry: row.SourcePersonalDebtID}
	}
	if err := s.replaceTagsIfSent(ctx, tx, userID, id, req); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &out, nil
}

// replaceTagsIfSent — tag_ids in an edit replace the caller's tags on the
// edited row.
func (s *Service) replaceTagsIfSent(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, req UpdateRequest) error {
	ids, ok := req.TagIDsChange()
	if !ok {
		return nil
	}
	return s.store.ReplaceTagsTx(ctx, tx, userID, id, ids)
}

// authorizeRowMutation applies the shared-wallet co-ownership rules
// (spec §14/2) for edit/delete of a non-transfer row the caller can see:
//
//   - author + active member    → full edit (incl. category)
//   - author + membership ended → ROW_LOCKED (rule 3)
//   - other active member       → allowed EXCEPT category_id (rule 2)
//
// Non-members never reach here — GetByID's visibility already 404'd.
// Pass a zero-value UpdateRequest for delete (no category change).
func (s *Service) authorizeRowMutation(
	ctx context.Context, userID uuid.UUID, current *Transaction, req UpdateRequest,
) error {
	// Floating rows (no account) are always personal — only the author may mutate.
	if current.AccountID == nil {
		if current.UserID != userID {
			return ErrTxNotFound
		}
		return nil
	}
	active, err := s.store.IsActiveMember(ctx, *current.AccountID, userID)
	if err != nil {
		return err
	}
	isAuthor := current.UserID == userID
	if isAuthor {
		if !active {
			return ErrRowLocked
		}
		return s.assertWritable(ctx, current.AccountID)
	}
	// Non-author: visibility guaranteed membership, but keep the guard.
	if !active {
		return ErrTxNotFound
	}
	if _, catChange := req.CategoryIDChange(); catChange {
		return ErrCategoryAuthorOnly
	}
	return s.assertWritable(ctx, current.AccountID)
}

// assertWritable — rows on an archived wallet are read-only (409
// ACCOUNT_ARCHIVED): no edit, delete, tag or split change until restored.
func (s *Service) assertWritable(ctx context.Context, ids ...*uuid.UUID) error {
	for _, id := range ids {
		if id == nil {
			continue
		}
		archived, err := s.store.accountArchived(ctx, *id)
		if err != nil {
			return err
		}
		if archived {
			return ErrAccountArchived
		}
	}
	return nil
}

func (s *Service) updateSingleInTx(
	ctx context.Context, tx pgx.Tx, userID uuid.UUID,
	current *Transaction, req UpdateRequest,
) (*Transaction, error) {
	newAccID, accChange := req.AccountIDChange()

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

	effectiveAmount := current.Amount
	if req.Amount != nil {
		effectiveAmount = *req.Amount
	}

	if accChange {
		// Move wallet: reverse old account balance, apply to new account.
		if current.AccountID != nil {
			if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*current.AccountID}); err != nil {
				return nil, err
			}
			oldDelta := signedDelta(current.Type, current.Amount, false)
			if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *current.AccountID, -oldDelta); err != nil {
				return nil, err
			}
		}
		if newAccID != nil {
			if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*newAccID}); err != nil {
				return nil, err
			}
			if _, _, err := s.store.GetAccountBalanceTx(ctx, tx, userID, *newAccID); err != nil {
				return nil, err
			}
			newDelta := signedDelta(current.Type, effectiveAmount, false)
			if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *newAccID, newDelta); err != nil {
				return nil, err
			}
		}
	} else if req.Amount != nil && *req.Amount != current.Amount && current.AccountID != nil {
		// Amount-only change on an existing account.
		if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*current.AccountID}); err != nil {
			return nil, err
		}
		oldDelta := signedDelta(current.Type, current.Amount, false)
		newDelta := signedDelta(current.Type, *req.Amount, false)
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *current.AccountID, newDelta-oldDelta); err != nil {
			return nil, err
		}
	}

	descVal, descChanged := req.DescriptionChange()
	noteVal, noteChanged := req.NoteChange()
	return s.store.UpdateRowTx(ctx, tx, userID, current.ID,
		req.Amount, req.Date,
		newCatID, catChange,
		descVal, descChanged,
		noteVal, noteChanged,
		newAccID, accChange)
}

// transferPair loads both rows of current's transfer and tells OUT from IN
// by the AUTHOR's Transfer OUT system category (categories are per user).
func (s *Service) transferPair(ctx context.Context, current *Transaction) (outRow, inRow *Transaction, err error) {
	if current.TransferGroupID == nil {
		return nil, nil, fmt.Errorf("transfer row has no transfer_group_id")
	}
	pair, err := s.store.GetByGroupID(ctx, *current.TransferGroupID)
	if err != nil {
		return nil, nil, err
	}
	if len(pair) != 2 {
		return nil, nil, fmt.Errorf("transfer pair has %d rows; expected 2", len(pair))
	}
	outCat, err := s.cats.SystemFor(ctx, current.UserID, categories.SystemTransferOut)
	if err != nil {
		return nil, nil, err
	}
	for i := range pair {
		if pair[i].CategoryID != nil && *pair[i].CategoryID == outCat.ID {
			outRow = &pair[i]
		} else {
			inRow = &pair[i]
		}
	}
	if outRow == nil || inRow == nil {
		return nil, nil, fmt.Errorf("could not identify OUT/IN rows of transfer")
	}
	return outRow, inRow, nil
}

// updateTransferInTx applies amount/date/text/wallet changes to both rows
// of a transfer in one tx, re-balancing the accounts atomically. Returns
// (outRow, inRow, every wallet touched before and after).
func (s *Service) updateTransferInTx(
	ctx context.Context, tx pgx.Tx, userID uuid.UUID,
	current *Transaction, req UpdateRequest,
) (*Transaction, *Transaction, []uuid.UUID, error) {
	if _, catChange := req.CategoryIDChange(); catChange {
		return nil, nil, nil, ErrTransferCategoryEdit
	}
	outRow, inRow, err := s.transferPair(ctx, current)
	if err != nil {
		return nil, nil, nil, err
	}

	// A transfer mutation moves money on BOTH accounts — the caller needs
	// active membership on both (shared-wallet rules §14/2; ex-author →
	// ROW_LOCKED, foreign non-member side → not found).
	if err := s.authorizePairMutation(ctx, userID, current, outRow, inRow); err != nil {
		return nil, nil, nil, err
	}

	// Each leg is re-filed on its own: account_id = the OUT leg's wallet,
	// transfer_to_account_id = the IN leg's. A leg that has a wallet can't be
	// cleared (a wallet-less leg only comes from deleting its wallet); a
	// wallet-less leg can be given one.
	newOutAccID, outAccChange := req.AccountIDChange()
	newInAccID, inAccChange := req.TransferToAccountIDChange()
	if outAccChange && newOutAccID == nil {
		if outRow.AccountID != nil {
			return nil, nil, nil, ErrTransferRequiresAccount
		}
		outAccChange = false
	}
	if inAccChange && newInAccID == nil {
		if inRow.AccountID != nil {
			return nil, nil, nil, ErrTransferRequiresAccount
		}
		inAccChange = false
	}
	effOut, effIn := outRow.AccountID, inRow.AccountID
	if outAccChange {
		effOut = newOutAccID
	}
	if inAccChange {
		effIn = newInAccID
	}
	if effOut != nil && effIn != nil && *effOut == *effIn {
		return nil, nil, nil, ErrTransferSameAccount
	}

	effectiveAmount := current.Amount
	if req.Amount != nil {
		effectiveAmount = *req.Amount
	}

	// Every account involved (old + new), for locking and the response.
	touched := nonNilIDs(outRow.AccountID, inRow.AccountID, newOutAccID, newInAccID)
	lockIDs := uniqueUUIDs(touched)
	sort.Slice(lockIDs, func(i, j int) bool { return uuidLess(lockIDs[i], lockIDs[j]) })
	if len(lockIDs) > 0 {
		if err := s.store.LockAccountsForUpdate(ctx, tx, lockIDs); err != nil {
			return nil, nil, nil, err
		}
	}

	// Balances move per leg; a wallet-less leg moves nothing.
	// sign: OUT leg loses the amount, IN leg gains it.
	legs := []struct {
		old, neu *uuid.UUID
		moved    bool
		sign     float64
	}{
		{outRow.AccountID, newOutAccID, outAccChange, -1},
		{inRow.AccountID, newInAccID, inAccChange, +1},
	}
	for _, l := range legs {
		switch {
		case l.moved:
			if l.old != nil {
				if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *l.old, -l.sign*current.Amount); err != nil {
					return nil, nil, nil, err
				}
			}
			if _, _, err := s.store.GetAccountBalanceTx(ctx, tx, userID, *l.neu); err != nil {
				return nil, nil, nil, err
			}
			if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *l.neu, l.sign*effectiveAmount); err != nil {
				return nil, nil, nil, err
			}
		case req.Amount != nil && *req.Amount != current.Amount && l.old != nil:
			if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *l.old, l.sign*(effectiveAmount-current.Amount)); err != nil {
				return nil, nil, nil, err
			}
		}
	}

	descVal, descChanged := req.DescriptionChange()
	noteVal, noteChanged := req.NoteChange()

	updatedOut, err := s.store.UpdateRowTx(ctx, tx, userID, outRow.ID,
		req.Amount, req.Date, nil, false, descVal, descChanged, noteVal, noteChanged, newOutAccID, outAccChange)
	if err != nil {
		return nil, nil, nil, err
	}
	updatedIn, err := s.store.UpdateRowTx(ctx, tx, userID, inRow.ID,
		req.Amount, req.Date, nil, false, descVal, descChanged, noteVal, noteChanged, newInAccID, inAccChange)
	if err != nil {
		return nil, nil, nil, err
	}
	return updatedOut, updatedIn, touched, nil
}

// --- Delete ---

// Delete removes a row (both rows of a transfer) and reverses its balance.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	_, err := s.delete(ctx, userID, id, false)
	return err
}

// deleted — what a delete did: the wallets it moved money on and the debt
// entry that went with the row (its person key, for the new balance).
type deleted struct {
	touched   []uuid.UUID
	debtEntry *uuid.UUID
	debtKey   string
	item      *uuid.UUID
	bill      *uuid.UUID
}

// DeleteWrite — DELETE /transactions/:id: the deleted id and the
// post-commit balances of every wallet the delete touched.
// alsoDeleteLink — the recurring item / event bill linked to the row go too.
func (s *Service) DeleteWrite(ctx context.Context, userID, id uuid.UUID, alsoDeleteLink bool) (*DeleteResult, error) {
	d, err := s.delete(ctx, userID, id, alsoDeleteLink)
	if err != nil {
		return nil, err
	}
	balances, err := shared.LoadWalletBalances(ctx, s.store.db, d.touched)
	if err != nil {
		return nil, err
	}
	out := &DeleteResult{ID: id, AccountBalances: balances, DebtEntryID: d.debtEntry,
		RecurringItemID: d.item, BillID: d.bill}
	if d.debtEntry != nil && s.debtLinks != nil {
		if out.PersonBalance, err = s.debtLinks.PersonBalanceByKey(ctx, userID, d.debtKey); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// delete removes the row (both rows of a transfer; a debt entry's wallet
// side takes its entry along).
func (s *Service) delete(ctx context.Context, userID, id uuid.UUID, alsoDeleteLink bool) (*deleted, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	out := &deleted{}
	// The linked records first: the delete would break their links.
	if alsoDeleteLink && current.UserID == userID {
		if s.recurring != nil {
			if out.item, err = s.recurring.TxDeletingTx(ctx, tx, userID, current.ID); err != nil {
				return nil, err
			}
		}
		if s.billDeleter != nil && current.Type != TypeTransfer {
			if out.bill, err = s.billDeleter(ctx, tx, userID, current.ID); err != nil {
				return nil, err
			}
		}
	}
	if current.Type == TypeTransfer {
		if out.touched, err = s.deleteTransferInTx(ctx, tx, userID, current); err != nil {
			return nil, err
		}
	} else {
		// Shared-wallet authz: same matrix as Update (delete never
		// carries a category change, hence the zero-value request).
		if err := s.authorizeRowMutation(ctx, userID, current, UpdateRequest{}); err != nil {
			return nil, err
		}
		// Non-transfer with a system category (Opening Balance,
		// Adjustment) is undeletable — silently dropping it would
		// leave the cached `accounts.balance` honest but the audit
		// trail would lie. Reverse via the account-level operation.
		if current.SourcePersonalDebtID != nil {
			if err := checkDebtTxEdit(userID, current, UpdateRequest{}, true); err != nil {
				return nil, err
			}
		} else if err := s.checkNotSystemRow(ctx, current); err != nil {
			return nil, err
		}
		if err := s.deleteSingleInTx(ctx, tx, userID, current); err != nil {
			return nil, err
		}
		// A debt entry's wallet side: the entry goes too (one record).
		if current.SourcePersonalDebtID != nil && s.debtLinks != nil {
			key, err := s.debtLinks.TxDeletedTx(ctx, tx, userID, *current.SourcePersonalDebtID)
			if err != nil {
				return nil, err
			}
			out.debtEntry, out.debtKey = current.SourcePersonalDebtID, key
		}
		out.touched = nonNilIDs(current.AccountID)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// authorizePairMutation is the transfer-pair variant of
// authorizeRowMutation: the caller must hold active membership on BOTH
// accounts of the pair. The row's author whose membership ended gets
// ROW_LOCKED; a non-author without membership on either side gets
// not-found (they shouldn't learn the pair exists).
func (s *Service) authorizePairMutation(
	ctx context.Context, userID uuid.UUID, current, outRow, inRow *Transaction,
) error {
	var activeOut, activeIn bool
	var err error
	if outRow.AccountID != nil {
		activeOut, err = s.store.IsActiveMember(ctx, *outRow.AccountID, userID)
		if err != nil {
			return err
		}
	}
	if inRow.AccountID != nil {
		activeIn, err = s.store.IsActiveMember(ctx, *inRow.AccountID, userID)
		if err != nil {
			return err
		}
	}
	if outRow.AccountID == nil && inRow.AccountID == nil {
		// Both floating — only author may mutate.
		if current.UserID != userID {
			return ErrTxNotFound
		}
		return nil
	}
	// A wallet-less leg (its wallet was deleted) needs no membership.
	if (outRow.AccountID == nil || activeOut) && (inRow.AccountID == nil || activeIn) {
		return s.assertWritable(ctx, outRow.AccountID, inRow.AccountID)
	}
	if current.UserID == userID {
		return ErrRowLocked
	}
	return ErrTxNotFound
}

// checkNotSystemRow rejects mutations on transactions whose category
// is a system kind (Opening Balance, Adjustment). Transfer rows have
// system categories too, but they're handled by the dedicated transfer
// path above this check; callers must gate by `current.Type !=
// TypeTransfer` themselves.
//
// The lookup is AUTHOR-scoped — categories belong to the row's author,
// so a shared-wallet co-member editing the owner's row must still trip
// on the owner's system categories.
func (s *Service) checkNotSystemRow(ctx context.Context, current *Transaction) error {
	if current.CategoryID == nil {
		return nil
	}
	cat, err := s.cats.GetCategoryRow(ctx, current.UserID, *current.CategoryID)
	if err != nil {
		// If the category was hard-deleted out from under the row,
		// the FK is set NULL — we can't tell what kind it was. Allow.
		return nil
	}
	if cat.IsSystem {
		return ErrSystemTransactionImmutable
	}
	return nil
}

func (s *Service) deleteSingleInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, current *Transaction) error {
	if current.AccountID != nil {
		if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*current.AccountID}); err != nil {
			return err
		}
		delta := signedDelta(current.Type, current.Amount, false)
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *current.AccountID, -delta); err != nil {
			return err
		}
	}
	return s.store.DeleteRowTx(ctx, tx, current.ID)
}

// deleteTransferInTx deletes both rows of a transfer and reverses both
// balances; returns the wallets touched (OUT first).
func (s *Service) deleteTransferInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, current *Transaction) ([]uuid.UUID, error) {
	outRow, inRow, err := s.transferPair(ctx, current)
	if err != nil {
		return nil, err
	}
	if err := s.authorizePairMutation(ctx, userID, current, outRow, inRow); err != nil {
		return nil, err
	}

	touched := nonNilIDs(outRow.AccountID, inRow.AccountID)
	lockIDs := uniqueUUIDs(touched)
	sort.Slice(lockIDs, func(i, j int) bool { return uuidLess(lockIDs[i], lockIDs[j]) })
	if len(lockIDs) > 0 {
		if err := s.store.LockAccountsForUpdate(ctx, tx, lockIDs); err != nil {
			return nil, err
		}
	}

	// Reverse balances
	if outRow.AccountID != nil {
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *outRow.AccountID, current.Amount); err != nil {
			return nil, err
		}
	}
	if inRow.AccountID != nil {
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *inRow.AccountID, -current.Amount); err != nil {
			return nil, err
		}
	}

	// Delete both rows
	if err := s.store.DeleteRowTx(ctx, tx, outRow.ID); err != nil {
		return nil, err
	}
	if err := s.store.DeleteRowTx(ctx, tx, inRow.ID); err != nil {
		return nil, err
	}
	return touched, nil
}

// --- Tags ---

// SetTags — PUT /transactions/:id/tags: the caller's tags on the row become
// exactly tagIDs ([] clears). Same row rules as an edit (visible row; a row
// on a wallet the caller left is read-only). Returns the row, final state.
func (s *Service) SetTags(ctx context.Context, userID, id uuid.UUID, tagIDs []uuid.UUID) (*TransactionDetail, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeRowMutation(ctx, userID, current, UpdateRequest{}); err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := s.store.ReplaceTagsTx(ctx, tx, userID, id, tagIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.Get(ctx, userID, id)
}

// --- Summary ---

func (s *Service) Summary(ctx context.Context, userID uuid.UUID, req SummaryRequest) (*SummaryResponse, error) {
	// The central report-scope predicate (spec §14/5 guardrail) replaces
	// the old bare `user_id = $1` — every personal aggregate must go
	// through shared.ReportScopePredicate, never an inlined filter.
	// Reportable-category filter drops system rows (opening balance,
	// adjustments, transfers, debt received/paid) — same rule as the
	// per-account summary. Every query below joins categories as `c`.
	whereClauses := []string{
		shared.ReportScopePredicate("t", "$1"),
		shared.ReportableCategoryPredicate("c"),
		"t.date >= $2::date", "t.date <= $3::date",
		"t.type IN ('income','expense')",
	}
	args := []any{userID, req.From, req.To}

	if req.AccountID != nil {
		args = append(args, *req.AccountID)
		whereClauses = append(whereClauses, fmt.Sprintf("t.account_id = $%d", len(args)))
	}
	if req.CategoryID != nil {
		args = append(args, *req.CategoryID)
		whereClauses = append(whereClauses, fmt.Sprintf("t.category_id = $%d", len(args)))
	}
	if req.Type != nil {
		args = append(args, string(*req.Type))
		whereClauses = append(whereClauses, fmt.Sprintf("t.type = $%d", len(args)))
	}
	where := strings.Join(whereClauses, " AND ")

	var (
		totalIncome  float64
		totalExpense float64
		count        int
		currency     string
	)
	q := `SELECT
		COALESCE(SUM(` + shareT + `) FILTER (WHERE t.type = 'income'),  0),
		COALESCE(SUM(` + shareT + `) FILTER (WHERE t.type = 'expense'), 0),
		COUNT(*),
		COALESCE((SELECT currency FROM users WHERE id = $1), 'THB')
		FROM ` + summaryFrom + ` WHERE ` + where
	if err := s.store.db.QueryRow(ctx, q, args...).Scan(&totalIncome, &totalExpense, &count, &currency); err != nil {
		return nil, fmt.Errorf("summary: %w", err)
	}

	resp := &SummaryResponse{
		From:             req.From,
		To:               req.To,
		Currency:         currency, // user-level, locked
		TotalIncome:      totalIncome,
		TotalExpense:     totalExpense,
		Net:              totalIncome - totalExpense,
		TransactionCount: count,
		Groups:           []SummaryGroup{},
	}

	var err error
	switch req.GroupBy {
	case "category":
		resp.Groups, err = s.summaryByCategory(ctx, where, args, false)
	case "parent_category":
		resp.Groups, err = s.summaryByCategory(ctx, where, args, true)
	case "account":
		resp.Groups, err = s.summaryByAccount(ctx, where, args)
	case "day", "week", "month":
		resp.Groups, err = s.summaryByDate(ctx, where, args, req.GroupBy)
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// summaryFrom is the FROM clause every summary query shares — the
// categories join backs shared.ReportableCategoryPredicate("c").
const summaryFrom = `transactions t LEFT JOIN categories c ON c.id = t.category_id`

// shareT — each row's share-basis amount (spec 12 §4.5): summaries sum
// "my share", not the cash amount.
var shareT = shared.ShareAmountExpr("t")

// summaryAmounts is the shared aggregate tail: total, count, income, expense.
var summaryAmounts = `COALESCE(SUM(` + shareT + `), 0), COUNT(*),
		COALESCE(SUM(` + shareT + `) FILTER (WHERE t.type = 'income'),  0),
		COALESCE(SUM(` + shareT + `) FILTER (WHERE t.type = 'expense'), 0)`

// summaryByCategory groups by leaf category, or — rollup=true — by the
// top-level parent. Uncategorized rows come back as key "" / name "" /
// category null so the client can localise the label.
func (s *Service) summaryByCategory(ctx context.Context, where string, args []any, rollup bool) ([]SummaryGroup, error) {
	keyExpr, nameExpr, join := "c.id", "c.name", ""
	if rollup {
		keyExpr, nameExpr = "COALESCE(p.id, c.id)", "COALESCE(p.name, c.name)"
		join = " LEFT JOIN categories p ON p.id = c.parent_id"
	}
	q := fmt.Sprintf(`SELECT COALESCE(%[1]s::text, ''), COALESCE(%[2]s, ''), %[3]s
		FROM %[4]s%[5]s
		WHERE %[6]s
		GROUP BY %[1]s, %[2]s
		ORDER BY 3 DESC`, keyExpr, nameExpr, summaryAmounts, summaryFrom, join, where)
	groups, err := s.scanSummary(ctx, q, args)
	if err != nil {
		return nil, err
	}
	refs, err := shared.LoadCategoryRefs(ctx, s.store.db, groupKeyIDs(groups))
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if id, err := uuid.Parse(groups[i].Key); err == nil {
			if r, ok := refs[id]; ok {
				groups[i].Category = &r
			}
		}
	}
	return groups, nil
}

func (s *Service) summaryByAccount(ctx context.Context, where string, args []any) ([]SummaryGroup, error) {
	q := `SELECT a.id::text, a.name, ` + summaryAmounts + `
		FROM ` + summaryFrom + `
		JOIN accounts a ON a.id = t.account_id
		WHERE ` + where + `
		GROUP BY a.id, a.name
		ORDER BY 3 DESC`
	groups, err := s.scanSummary(ctx, q, args)
	if err != nil {
		return nil, err
	}
	refs, err := shared.LoadAccountRefs(ctx, s.store.db, groupKeyIDs(groups))
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if id, err := uuid.Parse(groups[i].Key); err == nil {
			if r, ok := refs[id]; ok {
				groups[i].Account = &r
			}
		}
	}
	return groups, nil
}

func groupKeyIDs(groups []SummaryGroup) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(groups))
	for _, g := range groups {
		if id, err := uuid.Parse(g.Key); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
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
		'', %s
		FROM %s
		WHERE %s
		GROUP BY date_trunc('%s', t.date)
		ORDER BY 1 ASC`, trunc, summaryAmounts, summaryFrom, where, trunc)
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
		if err := rows.Scan(&g.Key, &g.Name, &g.Total, &g.Count, &g.Income, &g.Expense); err != nil {
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

// nonNilIDs — the set ids, in order (duplicates kept; callers dedupe).
func nonNilIDs(ids ...*uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id != nil {
			out = append(out, *id)
		}
	}
	return out
}

// splitParentTx — what the debt hooks need of the caller's txID: its type,
// currency (wallet's, else the user's) and description.
func splitParentTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (typ, currency string, description *string, err error) {
	err = tx.QueryRow(ctx, `
		SELECT t.type, COALESCE(a.currency, u.currency, 'THB'), t.description
		FROM transactions t
		JOIN users u ON u.id = t.user_id
		LEFT JOIN accounts a ON a.id = t.account_id
		WHERE t.id = $1 AND t.user_id = $2`, txID, userID).Scan(&typ, &currency, &description)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrTxNotFound
	}
	return
}

// AddSplitsTx gives the caller's expense / income txID these split debts,
// inside tx — the same path as create, so linked contacts hear
// split_created. An event removal moves the board's member splits back
// this way (owner 2026-10-10).
func (s *Service) AddSplitsTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, splits []SplitInput) error {
	if len(splits) == 0 {
		return nil
	}
	if s.debtsCreator == nil {
		return ErrSplitsNotSupportedYet
	}
	typ, currency, description, err := splitParentTx(ctx, tx, userID, txID)
	if err != nil {
		return err
	}
	return s.debtsCreator(ctx, tx, txID, userID, typ, currency, description, splits)
}

// ClearSplitsTx removes all of txID's split debts, inside tx — the usual
// removal (linked partners get split_changed / their pending split_created
// is superseded). A pull onto an event board moves the splits there.
func (s *Service) ClearSplitsTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) error {
	if s.splitsEditor == nil {
		return ErrSplitsNotSupportedYet
	}
	typ, currency, description, err := splitParentTx(ctx, tx, userID, txID)
	if err != nil {
		return err
	}
	return s.splitsEditor(ctx, tx, txID, userID, typ, currency, description, nil)
}

// EditSplits — PUT /v1/transactions/:id/splits (owner 2026-10-10): the
// whole new split list for an expense / income the caller wrote. The debt
// rows (and the linked partners' notifications) are personal_debts'
// business, reached through the SplitsEditor hook inside this tx. Returns
// the row, final state (its splits are the new list).
func (s *Service) EditSplits(ctx context.Context, userID, id uuid.UUID, edits []SplitEdit) (*TransactionDetail, error) {
	if s.splitsEditor == nil {
		return nil, ErrSplitsNotSupportedYet
	}
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if current.UserID != userID {
		return nil, ErrSplitsAuthorOnly
	}
	if current.Type == TypeTransfer {
		return nil, ErrSplitsOnTransfer
	}
	// A debt entry's wallet side never carries splits.
	if current.SourcePersonalDebtID != nil {
		return nil, ErrSystemTransactionImmutable
	}
	if err := s.checkNotSystemRow(ctx, current); err != nil {
		return nil, err
	}
	var sum float64
	for _, e := range edits {
		sum += e.OwedAmount
	}
	if sum > current.Amount+0.005 {
		return nil, ErrSplitsExceedAmount
	}
	// An event bill's own splits are a layer on my share of it (owner
	// 2026-10-10): they can't go past amount − the board's member splits.
	board, err := s.store.EventSplitsTotal(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	if board > 0 && sum > current.Amount-board+0.005 {
		return nil, ErrSplitsExceedShare
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	// The bill's currency: its wallet's, else the user's.
	var currency string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE((SELECT currency FROM accounts WHERE id = $1),
		                (SELECT currency FROM users WHERE id = $2), 'THB')`,
		current.AccountID, userID).Scan(&currency); err != nil {
		return nil, fmt.Errorf("currency: %w", err)
	}
	edits, err = contactNamesTx(ctx, tx, userID, current.ID, edits)
	if err != nil {
		return nil, err
	}
	if err := s.splitsEditor(ctx, tx, current.ID, userID, string(current.Type), currency, current.Description, edits); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.Get(ctx, userID, id)
}

// contactNamesTx applies the person rule to an edit list (owner
// 2026-10-12): a split WITH a contact takes the contact's name — a sent
// person_name is ignored. Returns a copy where, per row:
//   - kept split, same contact          → person_name "" (unchanged)
//   - kept / new split, a new contact   → the contact's resolved name (the
//     stored snapshot follows the contact)
//   - kept split back to a free name    → person_name as sent; blank keeps
//     the name the contact showed
//   - free-name split                   → person_name as sent (rename)
//
// An unknown debt_id or contact is left for the editor hook to reject.
func contactNamesTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, edits []SplitEdit) ([]SplitEdit, error) {
	if len(edits) == 0 {
		return edits, nil
	}
	rows, err := tx.Query(ctx, `SELECT id, counterparty_contact_id FROM personal_debts
		WHERE source_transaction_id = $1 AND user_id = $2`, txID, userID)
	if err != nil {
		return nil, fmt.Errorf("split contacts: %w", err)
	}
	stored := map[uuid.UUID]*uuid.UUID{}
	var contactIDs []uuid.UUID
	for rows.Next() {
		var (
			id      uuid.UUID
			contact *uuid.UUID
		)
		if err := rows.Scan(&id, &contact); err != nil {
			rows.Close()
			return nil, fmt.Errorf("split contacts scan: %w", err)
		}
		stored[id] = contact
		if contact != nil {
			contactIDs = append(contactIDs, *contact)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, e := range edits {
		if e.ContactID != nil {
			contactIDs = append(contactIDs, *e.ContactID)
		}
	}
	persons, err := shared.LoadPersons(ctx, tx, userID, contactIDs)
	if err != nil {
		return nil, err
	}
	nameOf := func(contactID uuid.UUID) string {
		if p, ok := persons[contactID]; ok {
			return p.Name
		}
		return ""
	}

	out := make([]SplitEdit, len(edits))
	copy(out, edits)
	for i := range out {
		e := &out[i]
		if e.DebtID == nil {
			if e.ContactID != nil {
				if n := nameOf(*e.ContactID); n != "" {
					e.PersonName = n
				}
			}
			continue
		}
		before, known := stored[*e.DebtID]
		if !known {
			continue
		}
		after := before
		if e.ContactIDSet || e.ContactID != nil {
			after = e.ContactID
		}
		switch {
		case after != nil && before != nil && *after == *before:
			e.PersonName = ""
		case after != nil:
			e.PersonName = nameOf(*after)
		case before != nil && strings.TrimSpace(e.PersonName) == "":
			e.PersonName = nameOf(*before)
		}
	}
	return out, nil
}
