package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

var (
	ErrCreditFieldMismatch    = errors.New("credit_limit is required for credit_card / pay_later types; other types must not set credit fields")
	ErrTypeChangeNeedsLimit   = errors.New("changing account type to a credit type requires credit_limit")
	ErrBalanceNotEditable     = errors.New("balance is not directly editable; use POST /v1/accounts/:id/adjust-balance")
	ErrAdjustNoOp             = errors.New("new_balance equals current balance — nothing to adjust")
)

type Service struct {
	store *Store
	txs   *transactions.Service
	cats  *categories.Service
}

func NewService(s *Store, txs *transactions.Service, cats *categories.Service) *Service {
	return &Service{store: s, txs: txs, cats: cats}
}

// --- Reads ---

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*Account, error) {
	return s.store.GetByID(ctx, userID, id)
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, status, accType string) ([]Account, error) {
	if status == "" {
		status = StatusActive
	}
	return s.store.List(ctx, userID, status, accType)
}

// Summary computes income/expense/net for an account in a date range,
// filtered to **reportable** transactions only — categories with
// `include_in_report = false` (Opening Balance, Adjustment, Transfer
// In/Out, Lending, Reimbursements) are excluded. Spec §03/§2.7 +
// §05/§4.14c. Uncategorized rows count (still real activity).
func (s *Service) Summary(ctx context.Context, userID, accountID uuid.UUID, from, to string) (*SummaryResponse, error) {
	a, err := s.store.GetByID(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	income, expense, count, err := s.store.SummaryAggregate(ctx, userID, accountID, from, to)
	if err != nil {
		return nil, err
	}
	return &SummaryResponse{
		AccountID:        a.ID,
		Balance:          a.Balance,
		Currency:         a.Currency,
		From:             from,
		To:               to,
		TotalIncome:      income,
		TotalExpense:     expense,
		Net:              income - expense,
		TransactionCount: count,
	}, nil
}

// --- Create ---

func (s *Service) Create(ctx context.Context, userID uuid.UUID, userCurrency string, req CreateRequest) (*Account, error) {
	if err := s.validateCreditFields(req.Type, req); err != nil {
		return nil, err
	}

	currency := userCurrency
	if req.Currency != nil && *req.Currency != "" {
		currency = *req.Currency
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	row := &Account{
		UserID:         userID,
		Name:           strings.TrimSpace(req.Name),
		Type:           req.Type,
		Currency:       currency,
		Icon:           req.Icon,
		Color:          req.Color,
		Description:    req.Description,
		Note:           req.Note,
		CreditLimit:    req.CreditLimit,
		StatementDate:  req.StatementDate,
		PaymentDueDate: req.PaymentDueDate,
		MinimumPayment: req.MinimumPayment,
	}
	created, err := s.store.InsertTx(ctx, tx, row)
	if err != nil {
		return nil, err
	}

	// Opening balance auto-transaction
	if req.Balance != 0 {
		kind := categories.SystemOpeningIn
		amount := req.Balance
		if req.Balance < 0 {
			kind = categories.SystemOpeningOut
			amount = -req.Balance
		}
		today := time.Now().Format("2006-01-02")
		note := "Opening balance"
		_, newBalance, err := s.txs.CreateSystemInTx(ctx, tx, userID, created.ID, kind, amount, today, &note)
		if err != nil {
			return nil, err
		}
		created.Balance = newBalance
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return created, nil
}

// --- Update ---

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*Account, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	// Determine effective type after this update.
	effectiveType := current.Type
	if req.Type != nil {
		effectiveType = *req.Type
	}

	clearCreditFields := false
	if req.Type != nil && *req.Type != current.Type {
		// Switching TO credit: must include credit_limit
		if IsCreditType(effectiveType) && req.CreditLimit == nil && current.CreditLimit == nil {
			return nil, ErrTypeChangeNeedsLimit
		}
		// Switching FROM credit: clear billing fields
		if IsCreditType(current.Type) && !IsCreditType(effectiveType) {
			clearCreditFields = true
		}
	} else {
		// No type change. Block setting credit fields on a non-credit account.
		if !IsCreditType(effectiveType) {
			if req.CreditLimit != nil || req.StatementDate != nil ||
				req.PaymentDueDate != nil || req.MinimumPayment != nil {
				return nil, ErrCreditFieldMismatch
			}
		}
	}

	return s.store.Update(ctx, userID, id, req, clearCreditFields)
}

// --- Adjust balance ---

func (s *Service) AdjustBalance(ctx context.Context, userID, accountID uuid.UUID, req AdjustBalanceRequest) (*AdjustBalanceResponse, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := s.store.GetByID(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}

	delta := req.NewBalance - current.Balance
	if delta == 0 {
		return nil, ErrAdjustNoOp
	}
	kind := categories.SystemAdjustIn
	amount := delta
	if delta < 0 {
		kind = categories.SystemAdjustOut
		amount = -delta
	}

	date := time.Now().Format("2006-01-02")
	if req.Date != nil {
		date = *req.Date
	}
	note := "Balance adjustment"
	if req.Note != nil {
		note = *req.Note
	}

	created, newBalance, err := s.txs.CreateSystemInTx(ctx, tx, userID, accountID, kind, amount, date, &note)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	current.Balance = newBalance
	return &AdjustBalanceResponse{
		Account:                 current,
		AdjustmentTransactionID: created.ID,
	}, nil
}

// --- Archive ---

func (s *Service) Archive(ctx context.Context, userID, id uuid.UUID) error {
	return s.store.Archive(ctx, userID, id)
}

// --- Validation ---

func (s *Service) validateCreditFields(accType string, req CreateRequest) error {
	if IsCreditType(accType) {
		if req.CreditLimit == nil {
			return ErrCreditFieldMismatch
		}
		return nil
	}
	if req.CreditLimit != nil || req.StatementDate != nil ||
		req.PaymentDueDate != nil || req.MinimumPayment != nil {
		return ErrCreditFieldMismatch
	}
	return nil
}
