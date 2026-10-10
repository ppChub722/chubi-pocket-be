package pending

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/tags"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

const dateLayout = "2006-01-02"

// ErrInvalidDraft — a draft field is malformed (not merely missing).
var ErrInvalidDraft = errors.New("invalid draft")

// submitErr — a draft isn't ready to submit (missing / inconsistent).
type submitErr struct{ code, msg string }

func (e *submitErr) Error() string { return e.msg }

type Service struct {
	store *Store
	txs   *transactions.Service
	tags  *tags.Service
	debts *personal_debts.Service
}

func NewService(store *Store, txs *transactions.Service, tg *tags.Service, debts *personal_debts.Service) *Service {
	return &Service{store: store, txs: txs, tags: tg, debts: debts}
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) (*ListResponse, error) {
	rows, err := s.store.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &ListResponse{Data: rows, Count: len(rows)}, nil
}

// checkDraft — only shape errors; anything may still be missing.
func checkDraft(d Draft) error {
	if d.Type != nil {
		switch transactions.TxType(*d.Type) {
		case transactions.TypeExpense, transactions.TypeIncome, transactions.TypeTransfer:
		default:
			return fmt.Errorf("%w: type must be expense, income or transfer", ErrInvalidDraft)
		}
	}
	if d.Amount != nil && *d.Amount < 0 {
		return fmt.Errorf("%w: amount can't be negative", ErrInvalidDraft)
	}
	if d.Date != nil {
		if _, err := time.Parse(dateLayout, *d.Date); err != nil {
			return fmt.Errorf("%w: date must be YYYY-MM-DD", ErrInvalidDraft)
		}
	}
	return nil
}

// Create adds manual drafts (kind create). All or nothing.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, req CreateRequest) ([]PendingTransaction, error) {
	for _, it := range req.Items {
		if err := checkDraft(it.Draft); err != nil {
			return nil, err
		}
	}
	out := make([]PendingTransaction, 0, len(req.Items))
	for _, it := range req.Items {
		p, err := s.store.Insert(ctx, userID, SourceManual, KindCreate, it.Draft, nil, nil, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*PendingTransaction, error) {
	if err := checkDraft(req.Draft); err != nil {
		return nil, err
	}
	return s.store.UpdateDraft(ctx, userID, id, req.Draft)
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return s.store.Delete(ctx, userID, id)
}

// Submit turns each draft into its real effect, independently: one that
// fails stays pending with its reason; the others go through.
func (s *Service) Submit(ctx context.Context, userID uuid.UUID, req SubmitRequest) (*SubmitResponse, error) {
	out := &SubmitResponse{Submitted: []Submitted{}, Failed: []Failed{}}
	seen := map[uuid.UUID]bool{}
	for _, id := range req.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		p, err := s.store.Get(ctx, userID, id)
		if err != nil {
			out.Failed = append(out.Failed, Failed{ID: id, Error: toSubmitError(err)})
			continue
		}
		txID, err := s.submitOne(ctx, userID, p)
		if err != nil {
			e := toSubmitError(err)
			_ = s.store.SetError(ctx, userID, id, e)
			out.Failed = append(out.Failed, Failed{ID: id, Error: e})
			continue
		}
		out.Submitted = append(out.Submitted, Submitted{ID: id, TransactionID: txID})
	}
	return out, nil
}

func (s *Service) submitOne(ctx context.Context, userID uuid.UUID, p *PendingTransaction) (*uuid.UUID, error) {
	switch p.Kind {
	case KindCreate:
		return s.submitCreate(ctx, userID, p)
	case KindSettleDebt:
		return s.submitSettle(ctx, userID, p)
	case KindUpdateTx:
		return s.submitUpdate(ctx, userID, p)
	}
	return nil, fmt.Errorf("unknown pending kind %q", p.Kind)
}

// createRequest — the draft as a full POST /transactions body.
func createRequest(d Draft) (transactions.CreateRequest, error) {
	if d.Type == nil {
		return transactions.CreateRequest{}, &submitErr{"MISSING_TYPE", "type is required"}
	}
	if d.Amount == nil || *d.Amount <= 0 {
		return transactions.CreateRequest{}, &submitErr{"MISSING_AMOUNT", "amount must be more than 0"}
	}
	if d.Date == nil {
		return transactions.CreateRequest{}, &submitErr{"MISSING_DATE", "date is required"}
	}
	return transactions.CreateRequest{
		Type:                transactions.TxType(*d.Type),
		AccountID:           d.AccountID,
		Amount:              *d.Amount,
		CategoryID:          d.CategoryID,
		Date:                *d.Date,
		Description:         d.Description,
		Note:                d.Note,
		TransferToAccountID: d.TransferToAccountID,
		Splits:              d.Splits,
	}, nil
}

// submitCreate — transaction + tags + removing the draft, one DB tx.
func (s *Service) submitCreate(ctx context.Context, userID uuid.UUID, p *PendingTransaction) (*uuid.UUID, error) {
	req, err := createRequest(p.Draft)
	if err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	created, err := s.txs.CreateInTx(ctx, tx, userID, req)
	if err != nil {
		return nil, err
	}
	var txID uuid.UUID
	switch c := created.(type) {
	case *transactions.TransactionDetail:
		txID = c.ID
	case *transactions.TransferResponse:
		// Tags go on the OUT row, like the regular form.
		for _, r := range c.Rows {
			if r.AccountID != nil && req.AccountID != nil && *r.AccountID == *req.AccountID {
				txID = r.ID
			}
		}
	}
	if txID != uuid.Nil && s.tags != nil {
		if err := s.tags.AttachTagsTx(ctx, tx, userID, txID, p.Draft.TagIDs); err != nil {
			return nil, err
		}
	}
	if err := s.store.DeleteTx(ctx, tx, userID, p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &txID, nil
}

// submitSettle — a payment against TargetDebtID (wallet optional).
func (s *Service) submitSettle(ctx context.Context, userID uuid.UUID, p *PendingTransaction) (*uuid.UUID, error) {
	if s.debts == nil || p.TargetDebtID == nil {
		return nil, errors.New("debt settle not available")
	}
	if p.Draft.Amount == nil || *p.Draft.Amount <= 0 {
		return nil, &submitErr{"MISSING_AMOUNT", "amount must be more than 0"}
	}
	res, err := s.debts.Settle(ctx, userID, *p.TargetDebtID, personal_debts.SettleRequest{
		AccountID:   p.Draft.AccountID,
		Amount:      p.Draft.Amount,
		Date:        p.Draft.Date,
		Description: p.Draft.Description,
		Note:        p.Draft.Note,
	})
	if err != nil {
		return nil, err
	}
	_ = s.store.Delete(ctx, userID, p.ID)
	if d, ok := res.Transaction.(*transactions.TransactionDetail); ok && d != nil {
		return &d.ID, nil
	}
	return nil, nil
}

// submitUpdate — writes the draft's values onto TargetTransactionID.
func (s *Service) submitUpdate(ctx context.Context, userID uuid.UUID, p *PendingTransaction) (*uuid.UUID, error) {
	if p.TargetTransactionID == nil {
		return nil, errors.New("update target missing")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := s.txs.UpdateInTx(ctx, tx, userID, *p.TargetTransactionID, transactions.UpdateRequest{
		Amount:      p.Draft.Amount,
		Date:        p.Draft.Date,
		Description: p.Draft.Description,
		Note:        p.Draft.Note,
		CategoryID:  p.Draft.CategoryID,
		AccountID:   p.Draft.AccountID,
	}); err != nil {
		return nil, err
	}
	if err := s.store.DeleteTx(ctx, tx, userID, p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return p.TargetTransactionID, nil
}

// toSubmitError — a stable code + message the client can explain.
func toSubmitError(err error) SubmitError {
	var se *submitErr
	if errors.As(err, &se) {
		return SubmitError{Code: se.code, Message: se.msg}
	}
	codes := []struct {
		target error
		code   string
	}{
		{ErrPendingNotFound, "NOT_FOUND"},
		{transactions.ErrAccountForbidden, "ACCOUNT_NOT_FOUND"},
		{transactions.ErrCategoryForbidden, "CATEGORY_NOT_FOUND"},
		{transactions.ErrCategoryTypeMismatch, "CATEGORY_TYPE_MISMATCH"},
		{transactions.ErrSystemCategoryNotAllowed, "CATEGORY_NOT_FOUND"},
		{transactions.ErrTransferSameAccount, "TRANSFER_SAME_ACCOUNT"},
		{transactions.ErrTransferCurrencyMismatch, "TRANSFER_CURRENCY_MISMATCH"},
		{transactions.ErrTransferToAccountRequired, "TRANSFER_NEEDS_WALLETS"},
		{transactions.ErrTransferRequiresAccount, "TRANSFER_NEEDS_WALLETS"},
		{transactions.ErrSplitsOnTransfer, "SPLITS_ON_TRANSFER"},
		{transactions.ErrSplitContactNotFound, "CONTACT_NOT_FOUND"},
		{tags.ErrTagNotFound, "TAG_NOT_FOUND"},
		{personal_debts.ErrDebtNotFound, "DEBT_NOT_FOUND"},
		{personal_debts.ErrAlreadySettled, "DEBT_ALREADY_SETTLED"},
		{personal_debts.ErrOverpayment, "OVERPAYMENT"},
		{transactions.ErrTxNotFound, "TRANSACTION_NOT_FOUND"},
	}
	for _, c := range codes {
		if errors.Is(err, c.target) {
			return SubmitError{Code: c.code, Message: err.Error()}
		}
	}
	return SubmitError{Code: "SUBMIT_FAILED", Message: err.Error()}
}
