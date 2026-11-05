package transactions

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrAccountForbidden            = errors.New("account not found or not owned")
	ErrAccountArchived             = errors.New("wallet is archived: restore it first")
	ErrCategoryForbidden           = errors.New("category not found or not owned")
	ErrCategoryTypeMismatch        = errors.New("category type does not match transaction type")
	ErrTransferSameAccount         = errors.New("transfer source and destination must differ")
	ErrSystemCategoryNotAllowed    = errors.New("user category required (system categories are auto-assigned)")
	ErrTransferToAccountRequired   = apierr.Invalid("transfer_to_account_id is required for type=transfer", "transfer_to_account_id", apierr.FieldRequired)
	ErrTransferFieldsOnNonTransfer = apierr.Invalid("transfer_to_account_id is only valid for type=transfer", "transfer_to_account_id", apierr.FieldInvalid)
	ErrAmountInvalid               = apierr.Invalid("amount must be > 0", "amount", apierr.FieldOutOfRange, "gt", 0)
	ErrCategoryRequiredForTransfer = apierr.Invalid("transfer category is auto-set; do not pass category_id", "category_id", apierr.FieldInvalid)
	ErrTransferRequiresAccount     = apierr.Invalid("account_id is required for type=transfer", "account_id", apierr.FieldRequired)
	ErrTransferEditCurrency        = errors.New("cannot edit currency on a transfer; delete and recreate")
	ErrTransferCategoryEdit        = apierr.Invalid("cannot change category on a transfer; delete and recreate", "category_id", apierr.FieldInvalid)
	// tag_ids / PUT /:id/tags: every id must be one of the caller's tags.
	ErrTagNotFound                = errors.New("tag not found")
	ErrSystemTransactionImmutable = errors.New("transactions in a system category cannot be edited or deleted directly; use the account-level operation that created them")
	ErrCategoryAuthorOnly         = errors.New("only the row's author may change its category")
	ErrRowLocked                  = errors.New("membership on this account has ended; the row is read-only for you")
)

type Service struct {
	store     *Store
	cats      *categories.Service
	debtLinks DebtLinks
	recurring RecurringLinks
	papers    PaperReader
}

func NewService(s *Store, cats *categories.Service) *Service {
	return &Service{store: s, cats: cats}
}

func (s *Service) CountByCategory(ctx context.Context, categoryID uuid.UUID) (int, error) {
	return s.store.CountByCategory(ctx, categoryID)
}

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

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*TransactionDetail, error) {
	t, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	d, err := detailOf(ctx, s.store.db, userID, t)
	if err != nil {
		return nil, err
	}
	s.withPaper(ctx, userID, d)
	return d, nil
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

	from, to, today, err := s.listRange(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	f.RangeFrom, f.RangeTo = from, to
	rows, total, totals, daily, err := s.store.List(ctx, userID, f)
	if err != nil {
		return nil, err
	}

	if daily == nil {
		daily = []DailyPoint{}
	}
	avg := avgDailySpend(daily, from, to, today)
	data, err := buildDetails(ctx, s.store.db, userID, rows, false)
	if err != nil {
		return nil, err
	}
	return &ListResponse{
		Data:       data,
		Pagination: shared.NewPage(f.Page, f.PerPage, total),
		Totals:     totals,
		Daily:      daily, AvgDailySpend: avg,
	}, nil
}

func buildTransferResponse(
	ctx context.Context, q shared.Querier, userID, groupID uuid.UUID, outRow, inRow *Transaction,
) (*TransferResponse, error) {
	rows, err := buildDetails(ctx, q, userID, []Transaction{*outRow, *inRow}, true)
	if err != nil {
		return nil, err
	}
	return &TransferResponse{TransferGroupID: groupID, Rows: rows}, nil
}

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

func (s *Service) CreateWrite(ctx context.Context, userID uuid.UUID, req CreateRequest) (*WriteResult, error) {
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
	out, err := s.writeResult(ctx, rows, touched)
	if err != nil {
		return nil, err
	}
	if out.Effects, err = s.rowEffects(ctx, userID, rows, nil, nil, nil, out.AccountBalances); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) writeResult(ctx context.Context, rows []TransactionDetail, touched []uuid.UUID) (*WriteResult, error) {
	balances, err := shared.LoadWalletBalances(ctx, s.store.db, touched)
	if err != nil {
		return nil, err
	}
	return &WriteResult{Rows: rows, AccountBalances: balances}, nil
}

func (s *Service) CreateInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, req CreateRequest) (any, error) {
	if err := s.validateCreateCommon(req); err != nil {
		return nil, err
	}

	switch req.Type {
	case TypeExpense, TypeIncome:
		return s.createSingleInTx(ctx, tx, userID, req)
	case TypeTransfer:
		if req.AccountID == nil || req.TransferToAccountID == nil {
			return s.createOneSidedInTx(ctx, tx, userID, req)
		}
		return s.createTransferInTx(ctx, tx, userID, req)
	default:
		return nil, ErrAmountInvalid // unreachable; binding catches this
	}
}

func (s *Service) validateCreateCommon(req CreateRequest) error {
	if req.Amount < 0 || (req.Amount == 0 && !req.AllowZero) {
		return ErrAmountInvalid
	}
	if req.Type == TypeTransfer {
		// One side is enough: from only = ถอนเงิน, to only = เพิ่มเงิน.
		if req.AccountID == nil && req.TransferToAccountID == nil {
			return ErrTransferRequiresAccount
		}
		if req.AccountID != nil && req.TransferToAccountID != nil &&
			*req.TransferToAccountID == *req.AccountID {
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
	created, err := s.insertSingleTx(ctx, tx, userID, &Transaction{
		UserID: userID, AccountID: req.AccountID, Type: req.Type, Amount: req.Amount, CategoryID: req.CategoryID,
		Date: req.Date, Description: req.Description, Note: req.Note,
	}, req.TagIDs)
	if err != nil {
		return nil, err
	}
	return detailOf(ctx, tx, userID, created)
}

// createOneSidedInTx — a transfer with one wallet only: one row in WITHDRAW (from only) or
// DEPOSIT (to only), like an adjustment; out of reports.
func (s *Service) createOneSidedInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, req CreateRequest) (*TransactionDetail, error) {
	kind, accountID := categories.SystemWithdraw, req.AccountID
	if accountID == nil {
		kind, accountID = categories.SystemDeposit, req.TransferToAccountID
	}
	created, _, err := s.CreateSystemInTx(ctx, tx, userID, *accountID, kind, req.Amount, req.Date, req.Description, req.Note)
	if err != nil {
		return nil, err
	}
	if err := s.store.AttachTagsTx(ctx, tx, userID, created.ID, req.TagIDs); err != nil {
		return nil, err
	}
	return detailOf(ctx, tx, userID, created)
}

// insertSingleTx — an expense / income row: wallet locked and moved, category checked, tags set.
func (s *Service) insertSingleTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *Transaction, tagIDs []uuid.UUID) (*Transaction, error) {
	if row.AccountID != nil {
		if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*row.AccountID}); err != nil {
			return nil, err
		}
		if _, _, err := s.store.GetAccountBalanceTx(ctx, tx, userID, *row.AccountID); err != nil {
			return nil, err
		}
	}
	if row.CategoryID != nil {
		c, err := s.cats.GetCategoryRow(ctx, userID, *row.CategoryID)
		if err != nil {
			return nil, ErrCategoryForbidden
		}
		if c.IsSystem {
			return nil, ErrSystemCategoryNotAllowed
		}
		if c.Type != string(row.Type) {
			return nil, ErrCategoryTypeMismatch
		}
		row.CategoryID = &c.ID
	}
	created, err := s.store.InsertRowTx(ctx, tx, row)
	if err != nil {
		return nil, err
	}
	if row.AccountID != nil {
		delta := signedDelta(row.Type, created.Cash(), false)
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *row.AccountID, delta); err != nil {
			return nil, err
		}
	}
	if err := s.store.AttachTagsTx(ctx, tx, userID, created.ID, tagIDs); err != nil {
		return nil, err
	}
	return created, nil
}

// PaperBook — my book of a paper (a claim): the row the paper core writes.
type PaperBook struct {
	PaperID, PersonID uuid.UUID
	Type              TxType
	Amount            float64
	MyShare           *float64
	AccountID         *uuid.UUID
	CategoryID        *uuid.UUID
	Date              string
	Description, Note *string
	TagIDs            []uuid.UUID
}

// InsertPaperBookTx — writes my transaction for a paper (claim).
func (s *Service) InsertPaperBookTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, b PaperBook) (*Transaction, error) {
	paper, person := b.PaperID, b.PersonID
	return s.insertSingleTx(ctx, tx, userID, &Transaction{
		UserID: userID, AccountID: b.AccountID, Type: b.Type, Amount: b.Amount, CategoryID: b.CategoryID,
		Date: b.Date, Description: b.Description, Note: b.Note,
		SplitTransactionID: &paper, SplitPersonID: &person, MyShare: b.MyShare,
	}, b.TagIDs)
}

// SyncPaperBookTx — "อัปเดต": my book of a paper takes the paper's amount / my share / date /
// description (the wallet moves by the difference).
func (s *Service) SyncPaperBookTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID,
	amount float64, myShare *float64, date string, description *string) error {
	cur, err := s.store.GetForUpdateTx(ctx, tx, userID, txID)
	if err != nil {
		return err
	}
	if cur.AccountID != nil {
		if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*cur.AccountID}); err != nil {
			return err
		}
		diff := signedDelta(cur.Type, amount, false) - signedDelta(cur.Type, cur.Amount, false)
		if math.Abs(diff) > 0.0001 {
			if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *cur.AccountID, diff); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE transactions SET amount = $3, my_share = $4, date = $5::date, description = $6,
		updated_by_user_id = $2 WHERE id = $1`, txID, userID, amount, myShare, date, description)
	if err != nil {
		return fmt.Errorf("sync paper book: %w", err)
	}
	return nil
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

	// Membership gate on both wallets; currency is a label, never compared.
	for _, id := range []uuid.UUID{src, dst} {
		if _, _, err := s.store.GetAccountBalanceTx(ctx, tx, userID, id); err != nil {
			return nil, err
		}
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

	if err := s.store.AttachTagsTx(ctx, tx, userID, outRow.ID, req.TagIDs); err != nil {
		return nil, err
	}
	return buildTransferResponse(ctx, tx, userID, groupID, outRow, inRow)
}

func (s *Service) UpdateInTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, req UpdateRequest) error {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return err
	}
	if current.Type == TypeTransfer {
		return ErrTransferEditCurrency
	}
	if err := s.authorizeRowMutation(ctx, userID, current, req); err != nil {
		return err
	}
	// A debt entry's wallet side is edited from the entry (system row here); a repayment is editable.
	var check error
	if current.SourcePersonalDebtID != nil {
		check = s.checkNotSystemRow(ctx, current)
	} else {
		check = s.checkLinkedEdit(ctx, userID, current, req, false)
	}
	if check != nil {
		return check
	}
	if _, err = s.updateSingleInTx(ctx, tx, userID, current, req); err != nil {
		return err
	}
	return s.replaceTagsIfSent(ctx, tx, userID, id, req)
}

type updated struct {
	rows      []Transaction
	group     *uuid.UUID // transfers
	touched   []uuid.UUID
	debtEntry *uuid.UUID
	debtKey   string // a repayment's person
	// personKeys — the debt people the row touched before / after.
	personKeys []string
}

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

func (s *Service) UpdateWrite(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*WriteResult, error) {
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
	switch {
	case s.debtLinks == nil:
	case u.debtEntry != nil:
		if out.PersonBalance, err = s.debtLinks.PersonBalanceByEntry(ctx, userID, *u.debtEntry); err != nil {
			return nil, err
		}
	case u.debtKey != "":
		if out.PersonBalance, err = s.debtLinks.PersonBalanceByKey(ctx, userID, u.debtKey); err != nil {
			return nil, err
		}
	}
	if s.debtLinks != nil {
		for _, k := range u.personKeys {
			b, err := s.debtLinks.PersonBalanceByKey(ctx, userID, k)
			if err != nil {
				return nil, err
			}
			out.PersonBalances = append(out.PersonBalances, b)
		}
	}
	var debtIDs []uuid.UUID
	if u.debtEntry != nil {
		debtIDs = append(debtIDs, *u.debtEntry)
	}
	keys := append(append([]string{}, u.personKeys...), u.debtKey)
	if out.Effects, err = s.rowEffects(ctx, userID, rows, nil, debtIDs, keys, out.AccountBalances); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*updated, error) {
	u, _, err := s.updateOrPreview(ctx, userID, id, req, false)
	return u, err
}

// Preview — PUT /transactions/{id}?preview=true: the whole edit runs in a savepoint and is rolled
// back; the answer says what it would do (the row, every touched person's and wallet's balance).
func (s *Service) Preview(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*PreviewResult, error) {
	_, p, err := s.updateOrPreview(ctx, userID, id, req, true)
	return p, err
}

func (s *Service) updateOrPreview(ctx context.Context, userID, id uuid.UUID, req UpdateRequest, preview bool) (*updated, *PreviewResult, error) {
	outer, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin tx: %w", err)
	}
	defer outer.Rollback(ctx)
	tx := outer
	if preview {
		if tx, err = outer.Begin(ctx); err != nil { // a savepoint: the "before" stays readable
			return nil, nil, fmt.Errorf("savepoint: %w", err)
		}
		defer tx.Rollback(ctx)
	}

	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, nil, err
	}

	var out updated
	if current.Type == TypeTransfer {
		outRow, inRow, touched, err := s.updateTransferInTx(ctx, tx, userID, current, req)
		if err != nil {
			return nil, nil, err
		}
		out = updated{rows: []Transaction{*outRow, *inRow}, group: current.TransferGroupID, touched: touched}
		if err := s.replaceTagsIfSent(ctx, tx, userID, id, req); err != nil {
			return nil, nil, err
		}
	} else {
		if err := s.authorizeRowMutation(ctx, userID, current, req); err != nil {
			return nil, nil, err
		}
		// Non-transfer with a system category (Opening Balance / Adjustment) is read-only — the user
		// can't directly edit the auto-created bookkeeping row; a debt's wallet side and a repayment
		// are the exceptions.
		if err := s.checkLinkedEdit(ctx, userID, current, req, false); err != nil {
			return nil, nil, err
		}
		// My book of a paper is mine to edit (one way: the paper never follows); the private note too.
		if note, ok := req.PrivateNoteChange(); ok {
			if err := setPrivateNoteTx(ctx, tx, userID, current, note); err != nil {
				return nil, nil, err
			}
		}
		row, err := s.updateSingleInTx(ctx, tx, userID, current, req)
		if err != nil {
			return nil, nil, err
		}
		if err := s.replaceTagsIfSent(ctx, tx, userID, id, req); err != nil {
			return nil, nil, err
		}
		if row.SourcePersonalDebtID != nil && s.debtLinks != nil {
			if err := s.debtLinks.TxEditedTx(ctx, tx, userID, *row.SourcePersonalDebtID, row); err != nil {
				return nil, nil, err
			}
		}
		if row, err = reloadTx(ctx, tx, id); err != nil {
			return nil, nil, err
		}
		out.rows, out.touched = []Transaction{*row}, nonNilIDs(current.AccountID, row.AccountID)
		out.debtEntry = row.SourcePersonalDebtID
	}
	if preview {
		p, err := s.previewResult(ctx, outer, tx, userID, &out)
		return nil, p, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit: %w", err)
	}
	return &out, nil, nil
}

func (s *Service) replaceTagsIfSent(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, req UpdateRequest) error {
	ids, ok := req.TagIDsChange()
	if !ok {
		return nil
	}
	return s.store.ReplaceTagsTx(ctx, tx, userID, id, ids)
}

func (s *Service) authorizeRowMutation(
	ctx context.Context, userID uuid.UUID, current *Transaction, req UpdateRequest,
) error {
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
	if !active {
		return ErrTxNotFound
	}
	if _, catChange := req.CategoryIDChange(); catChange {
		return ErrCategoryAuthorOnly
	}
	return s.assertWritable(ctx, current.AccountID)
}

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

	oldCash, newCash := current.Cash(), current.Amount
	if req.Amount != nil {
		newCash = *req.Amount
	}

	if accChange {
		if current.AccountID != nil {
			if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*current.AccountID}); err != nil {
				return nil, err
			}
			oldDelta := signedDelta(current.Type, oldCash, false)
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
			newDelta := signedDelta(current.Type, newCash, false)
			if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *newAccID, newDelta); err != nil {
				return nil, err
			}
		}
	} else if newCash != oldCash && current.AccountID != nil {
		if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*current.AccountID}); err != nil {
			return nil, err
		}
		oldDelta := signedDelta(current.Type, oldCash, false)
		newDelta := signedDelta(current.Type, newCash, false)
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

	if err := s.authorizePairMutation(ctx, userID, current, outRow, inRow); err != nil {
		return nil, nil, nil, err
	}

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

	touched := nonNilIDs(outRow.AccountID, inRow.AccountID, newOutAccID, newInAccID)
	lockIDs := uniqueUUIDs(touched)
	sort.Slice(lockIDs, func(i, j int) bool { return uuidLess(lockIDs[i], lockIDs[j]) })
	if len(lockIDs) > 0 {
		if err := s.store.LockAccountsForUpdate(ctx, tx, lockIDs); err != nil {
			return nil, nil, nil, err
		}
	}

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

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	_, err := s.delete(ctx, userID, id)
	return err
}

type deleted struct {
	touched   []uuid.UUID
	debtEntry *uuid.UUID
	debtKey   string
	item      *uuid.UUID
}

// DeleteWrite deletes a transaction. The recurring occurrence it fills is always skipped (0.4.1-1 R2).
func (s *Service) DeleteWrite(ctx context.Context, userID, id uuid.UUID) (*DeleteResult, error) {
	d, err := s.delete(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	balances, err := shared.LoadWalletBalances(ctx, s.store.db, d.touched)
	if err != nil {
		return nil, err
	}
	out := &DeleteResult{ID: id, AccountBalances: balances, DebtEntryID: d.debtEntry,
		RecurringItemID: d.item}
	if d.debtKey != "" && s.debtLinks != nil {
		if out.PersonBalance, err = s.debtLinks.PersonBalanceByKey(ctx, userID, d.debtKey); err != nil {
			return nil, err
		}
	}
	if out.Effects, err = s.rowEffects(ctx, userID, nil, []uuid.UUID{id}, nil, unionKeys([]string{d.debtKey}, nil), out.AccountBalances); err != nil {
		return nil, err
	}
	if d.debtEntry != nil { // the entry went with its tx (owner 2026-11-05)
		out.Effects.RemovedDebtIDs = append(out.Effects.RemovedDebtIDs, *d.debtEntry)
	}
	if d.item != nil && s.recurring != nil { // the occurrence it filled, now skipped (R2)
		if slot, err := s.recurring.SlotView(ctx, *d.item); err == nil {
			out.Effects.Slots = append(out.Effects.Slots, slot)
		}
	}
	return out, nil
}

func (s *Service) delete(ctx context.Context, userID, id uuid.UUID) (*deleted, error) {
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
	// The linked records first: the delete would break their links. The low-level delete runs the
	// recurring hook again for every path; here it also tells the answer which occurrence it was.
	if out.item, err = s.recurringDeletingTx(ctx, tx, current.ID); err != nil {
		return nil, err
	}
	// Deleting my book of a paper leaves the paper as it is (one way); I can claim it again.
	if current.Type == TypeTransfer {
		if out.touched, err = s.deleteTransferInTx(ctx, tx, userID, current); err != nil {
			return nil, err
		}
	} else {
		// Shared-wallet authz: same matrix as Update (delete never carries a category change, hence the
		// zero-value request).
		if err := s.authorizeRowMutation(ctx, userID, current, UpdateRequest{}); err != nil {
			return nil, err
		}
		// Non-transfer with a system category (Opening Balance, Adjustment) is undeletable — silently
		// dropping it would leave the cached `accounts.balance` honest but the audit trail would lie.
		if err := s.checkLinkedEdit(ctx, userID, current, UpdateRequest{}, true); err != nil {
			return nil, err
		}
		if err := s.deleteSingleInTx(ctx, tx, userID, current); err != nil {
			return nil, err
		}
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

// authorizePairMutation is the transfer-pair variant of authorizeRowMutation: the caller must hold
// active membership on BOTH accounts of the pair.
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
		if current.UserID != userID {
			return ErrTxNotFound
		}
		return nil
	}
	if (outRow.AccountID == nil || activeOut) && (inRow.AccountID == nil || activeIn) {
		return s.assertWritable(ctx, outRow.AccountID, inRow.AccountID)
	}
	if current.UserID == userID {
		return ErrRowLocked
	}
	return ErrTxNotFound
}

func (s *Service) checkNotSystemRow(ctx context.Context, current *Transaction) error {
	if current.CategoryID == nil {
		return nil
	}
	cat, err := s.cats.GetCategoryRow(ctx, current.UserID, *current.CategoryID)
	if err != nil {
		// If the category was hard-deleted out from under the row, the FK is set NULL — we can't tell
		// what kind it was.
		return nil
	}
	if cat.IsSystem && !isOneSided(cat) {
		return ErrSystemTransactionImmutable
	}
	return nil
}

// isOneSided — a ถอนเงิน / เพิ่มเงิน row: its own row to edit (amount, date, wallet, text, tags)
// or delete, never its category or wallet away.
func isOneSided(c *categories.Category) bool {
	return c.SystemKind != nil &&
		(*c.SystemKind == string(categories.SystemWithdraw) || *c.SystemKind == string(categories.SystemDeposit))
}

func (s *Service) checkOneSidedEdit(ctx context.Context, current *Transaction, req UpdateRequest) error {
	if current.CategoryID == nil {
		return nil
	}
	cat, err := s.cats.GetCategoryRow(ctx, current.UserID, *current.CategoryID)
	if err != nil || !isOneSided(cat) {
		return nil
	}
	if c, ok := req.CategoryIDChange(); ok && (c == nil || *c != cat.ID) {
		return ErrSystemTransactionImmutable
	}
	if acc, ok := req.AccountIDChange(); ok && acc == nil {
		return ErrTransferRequiresAccount
	}
	return nil
}

func (s *Service) deleteSingleInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, current *Transaction) error {
	if current.AccountID != nil {
		if err := s.store.LockAccountsForUpdate(ctx, tx, []uuid.UUID{*current.AccountID}); err != nil {
			return err
		}
		delta := signedDelta(current.Type, current.Cash(), false)
		if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, *current.AccountID, -delta); err != nil {
			return err
		}
	}
	if _, err := s.recurringDeletingTx(ctx, tx, current.ID); err != nil {
		return err
	}
	return s.store.DeleteRowTx(ctx, tx, current.ID)
}

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

	if _, err := s.recurringDeletingTx(ctx, tx, outRow.ID); err != nil { // finds either leg's occurrence
		return nil, err
	}
	if err := s.store.DeleteRowTx(ctx, tx, outRow.ID); err != nil {
		return nil, err
	}
	if err := s.store.DeleteRowTx(ctx, tx, inRow.ID); err != nil {
		return nil, err
	}
	return touched, nil
}

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

func (s *Service) Summary(ctx context.Context, userID uuid.UUID, req SummaryRequest) (*SummaryResponse, error) {
	whereClauses := []string{
		shared.ReportScopePredicate("t", "$1"),
		shared.ReportableCategoryPredicate("c"),
		"t.date >= $2::date", "t.date <= $3::date",
		"t.date <= " + shared.TodaySQL("$1"), // a future-dated transaction counts from its day
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
	if !req.GroupsOnly {
		q := `SELECT
		COALESCE(SUM(` + shareT + `) FILTER (WHERE t.type = 'income'),  0),
		COALESCE(SUM(` + shareT + `) FILTER (WHERE t.type = 'expense'), 0),
		COUNT(*),
		COALESCE((SELECT currency FROM users WHERE id = $1), 'THB')
		FROM ` + summaryFrom + ` WHERE ` + where
		if err := s.store.db.QueryRow(ctx, q, args...).Scan(&totalIncome, &totalExpense, &count, &currency); err != nil {
			return nil, fmt.Errorf("summary: %w", err)
		}
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

const summaryFrom = `transactions t LEFT JOIN categories c ON c.id = t.category_id`

var shareT = shared.ShareAmountExpr("t")

var summaryAmounts = `COALESCE(SUM(` + shareT + `), 0), COUNT(*),
		COALESCE(SUM(` + shareT + `) FILTER (WHERE t.type = 'income'),  0),
		COALESCE(SUM(` + shareT + `) FILTER (WHERE t.type = 'expense'), 0)`

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

func uuidLess(a, b uuid.UUID) bool {
	for i := 0; i < 16; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func nonNilIDs(ids ...*uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id != nil {
			out = append(out, *id)
		}
	}
	return out
}
