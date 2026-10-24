package accounts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrCreditFieldMismatch  = errors.New("credit_limit is required for credit_card / pay_later types; other types must not set credit fields")
	ErrTypeChangeNeedsLimit = errors.New("changing account type to a credit type requires credit_limit")
	ErrBalanceNotEditable   = errors.New("balance is not directly editable; use POST /v1/accounts/:id/adjust-balance")
	ErrAdjustNoOp           = errors.New("new_balance equals current balance — nothing to adjust")
	ErrBalanceNotTracked    = errors.New("this wallet doesn't track its balance; turn track_balance on first")
	ErrCurrentBalanceAlone  = errors.New("current_balance goes with track_balance: true")
)

type Service struct {
	store *Store
	txs   *transactions.Service
	cats  *categories.Service
	notifs *notifications.Service
}

func NewService(s *Store, txs *transactions.Service, cats *categories.Service) *Service {
	return &Service{store: s, txs: txs, cats: cats}
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*Account, error) {
	return s.getDecorated(ctx, userID, id, "")
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, status, accType string) ([]Account, error) {
	if status == "" {
		status = StatusActive
	}
	return s.store.List(ctx, userID, status, accType)
}

func (s *Service) ListPage(ctx context.Context, userID uuid.UUID, status, accType, tz string) (*AccountList, error) {
	accs, err := s.List(ctx, userID, status, accType)
	if err != nil {
		return nil, err
	}
	if err := s.decorate(ctx, userID, accs, tz); err != nil {
		return nil, err
	}
	currency, err := s.store.UserCurrency(ctx, userID)
	if err != nil {
		return nil, err
	}
	archived, err := s.store.CountArchived(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &AccountList{
		Data:          accs,
		Total:         len(accs),
		Totals:        buildTotals(accs, currency),
		ArchivedCount: archived,
	}, nil
}

func buildTotals(accs []Account, currency string) AccountTotals {
	t := AccountTotals{Currency: currency, ByType: []TypeTotal{}}
	byType := map[string]*TypeTotal{}
	for _, a := range accs {
		if a.Status == StatusArchived {
			continue // archived wallets are ignored in every total
		}
		if a.IsShared {
			t.SharedCount++
		}
		if !a.TrackBalance {
			t.UntrackedCount++
			continue
		}
		if a.IsShared {
			t.SharedBalance += a.Balance
		}
		tt := byType[a.Type]
		if tt == nil {
			tt = &TypeTotal{Type: a.Type}
			byType[a.Type] = tt
		}
		tt.Count++
		tt.Balance += a.Balance
		if a.Group == "liability" {
			t.Liabilities -= a.Balance
			t.CreditUsed -= a.Balance
			if a.CreditLimit != nil {
				t.CreditLimit += *a.CreditLimit
			}
		} else {
			t.Assets += a.Balance
		}
	}
	for _, typ := range []string{TypeCash, TypeBank, TypeEWallet, TypeCreditCard, TypePayLater} {
		if tt := byType[typ]; tt != nil {
			tt.Balance = round2(tt.Balance)
			t.ByType = append(t.ByType, *tt)
		}
	}
	t.Assets, t.Liabilities = round2(t.Assets), round2(t.Liabilities)
	t.CreditUsed, t.CreditLimit = round2(t.CreditUsed), round2(t.CreditLimit)
	t.SharedBalance = round2(t.SharedBalance)
	t.Net = round2(t.Assets - t.Liabilities)
	for _, a := range accs {
		if a.Status != StatusArchived && a.TrackBalance {
			t.SavedTotal += a.Savings.Saved
		}
	}
	t.SavedTotal = round2(t.SavedTotal)
	t.Total, t.CashTotal, t.CardDebt = round2(t.Assets-t.Liabilities), t.Assets, t.Liabilities
	if t.CreditLimit > 0 {
		t.CardUsedPct = int(math.Round(t.CreditUsed / t.CreditLimit * 100))
	}
	t.AvailableTotal = round2(t.CashTotal - t.SavedTotal)
	t.CardRemaining = round2(math.Max(0, t.CreditLimit-t.CardDebt))
	t.CashSplit = []CashShare{}
	for _, typ := range []string{TypeBank, TypeCash, TypeEWallet} {
		tt := byType[typ]
		if tt == nil {
			continue
		}
		pct := 0
		if t.CashTotal > 0 {
			pct = int(math.Round(tt.Balance / t.CashTotal * 100))
		}
		t.CashSplit = append(t.CashSplit, CashShare{Type: typ, Pct: pct})
	}
	return t
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func (s *Service) GetDetail(ctx context.Context, userID, id uuid.UUID, include []string, from, to, tz string) (*AccountDetail, error) {
	wantSummary, wantMembers, wantDelete := false, false, false
	for _, inc := range include {
		switch strings.TrimSpace(inc) {
		case "summary":
			wantSummary = true
		case "members":
			wantMembers = true
		case "delete_preview":
			wantDelete = true
		case "":
		default:
			return nil, ErrInvalidInclude
		}
	}
	for _, d := range []string{from, to} {
		if d == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return nil, ErrInvalidDate
		}
	}
	a, err := s.getDecorated(ctx, userID, id, tz)
	if err != nil {
		return nil, err
	}
	out := &AccountDetail{Account: *a}
	if wantSummary {
		if out.Summary, err = s.summaryOf(ctx, id, from, to); err != nil {
			return nil, err
		}
	}
	if wantDelete {
		if p, err := s.DeletePreview(ctx, userID, id); err == nil {
			out.DeletePreview = p
		} else if !errors.Is(err, ErrNotOwner) && !errors.Is(err, ErrAccountHasMembers) {
			return nil, err
		}
	}
	if wantMembers {
		if out.MembersHistory, err = s.store.ListMembersByAccount(ctx, userID, id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Summary is the wallet's income / expense over [from, to] (default: this month).
func (s *Service) Summary(ctx context.Context, userID, accountID uuid.UUID, from, to string) (*AccountSummary, error) {
	for _, d := range []string{from, to} {
		if d == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return nil, ErrInvalidDate
		}
	}
	ok, err := s.store.IsActiveMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrAccountNotFound
	}
	return s.summaryOf(ctx, accountID, from, to)
}

// summaryOf is Summary for a caller already known to be a member.
func (s *Service) summaryOf(ctx context.Context, accountID uuid.UUID, from, to string) (*AccountSummary, error) {
	now := time.Now()
	if from == "" {
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
	}
	if to == "" {
		to = time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
	}
	income, expense, count, err := s.store.SummaryAggregate(ctx, accountID, from, to)
	if err != nil {
		return nil, err
	}
	return &AccountSummary{
		From:             from,
		To:               to,
		TotalIncome:      income,
		TotalExpense:     expense,
		Net:              round2(income - expense),
		TransactionCount: count,
	}, nil
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, req CreateRequest) (*Account, error) {
	if err := s.validateCreditFields(req.Type, req); err != nil {
		return nil, err
	}

	currency, err := s.store.UserCurrency(ctx, userID)
	if err != nil {
		return nil, err
	}
	identifiers, err := NormalizeIdentifiers(req.Identifiers)
	if err != nil {
		return nil, err
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
		IconCode:       req.IconCode,
		LogoURL:        req.LogoURL,
		Description:    req.Description,
		Note:           req.Note,
		CreditLimit:    req.CreditLimit,
		StatementDate:  req.StatementDate,
		PaymentDueDate: req.PaymentDueDate,
		MinimumPayment: req.MinimumPayment,
		Identifiers:    identifiers,
		TrackBalance:   req.TrackBalance == nil || *req.TrackBalance,
	}
	created, err := s.store.InsertTx(ctx, tx, row)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	if _, err := s.store.InsertMemberTx(ctx, tx,
		created.ID, userID, MemberRoleOwner, ReportScopeAll, &now, userID,
	); err != nil {
		return nil, err
	}

	// Opening balance auto-transaction (none for a wallet without a balance)
	if req.Balance != 0 && row.TrackBalance {
		kind := categories.SystemOpeningIn
		amount := req.Balance
		if req.Balance < 0 {
			kind = categories.SystemOpeningOut
			amount = -req.Balance
		}
		today := time.Now().Format("2006-01-02")
		_, newBalance, err := s.txs.CreateSystemInTx(ctx, tx, userID, created.ID, kind, amount, today, nil, nil)
		if err != nil {
			return nil, err
		}
		created.Balance = newBalance
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.getDecorated(ctx, userID, created.ID, "")
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*Account, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if !current.CanEdit {
		return nil, ErrNotOwner
	}

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
		if IsCreditType(current.Type) && !IsCreditType(effectiveType) {
			clearCreditFields = true
		}
	} else {
		if !IsCreditType(effectiveType) {
			if req.CreditLimit != nil || req.StatementDate != nil ||
				req.PaymentDueDate != nil || req.MinimumPayment != nil {
				return nil, ErrCreditFieldMismatch
			}
		}
	}

	if req.Identifiers != nil {
		norm, err := NormalizeIdentifiers(*req.Identifiers)
		if err != nil {
			return nil, err
		}
		req.Identifiers = &norm
	}
	if req.CurrentBalance != nil && (req.TrackBalance == nil || !*req.TrackBalance) {
		return nil, ErrCurrentBalanceAlone
	}

	if _, err := s.store.Update(ctx, userID, id, req, clearCreditFields); err != nil {
		return nil, err
	}
	if req.CurrentBalance != nil {
		if _, err := s.AdjustBalance(ctx, userID, id, AdjustBalanceRequest{NewBalance: req.CurrentBalance}); err != nil &&
			!errors.Is(err, ErrAdjustNoOp) {
			return nil, err
		}
	}
	if req.SortOrder != nil {
		if err := s.store.SetMySortOrder(ctx, userID, id, *req.SortOrder); err != nil {
			return nil, err
		}
	}
	return s.getDecorated(ctx, userID, id, "")
}

func (s *Service) AddIdentifier(ctx context.Context, userID, id uuid.UUID, in Identifier) (*Account, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	list, err := NormalizeIdentifiers(append(slices.Clone(current.Identifiers), in))
	if err != nil {
		return nil, err
	}
	return s.Update(ctx, userID, id, UpdateRequest{Identifiers: &list})
}

func (s *Service) AdjustBalance(ctx context.Context, userID, accountID uuid.UUID, req AdjustBalanceRequest) (*AdjustBalanceResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := s.store.GetByID(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}

	if !current.TrackBalance {
		return nil, ErrBalanceNotTracked
	}
	delta := *req.NewBalance - current.Balance
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
	created, newBalance, err := s.txs.CreateSystemInTx(ctx, tx, userID, accountID, kind, amount, date, req.Description, req.Note)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	_ = newBalance

	acc, err := s.store.GetByID(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	row, err := s.txs.Get(ctx, userID, created.ID)
	if err != nil {
		return nil, err
	}
	balances, err := shared.LoadWalletBalances(ctx, s.store.Pool(), []uuid.UUID{accountID})
	if err != nil {
		return nil, err
	}
	return &AdjustBalanceResult{Account: acc, Transaction: row, AccountBalances: balances}, nil
}

func (s *Service) Archive(ctx context.Context, userID, id uuid.UUID) (*Account, error) {
	a, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if !a.CanEdit {
		return nil, ErrNotOwner
	}
	n, err := s.store.CountActiveMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	if n > 1 {
		return nil, ErrAccountHasMembers
	}
	if err := s.store.Archive(ctx, userID, id); err != nil {
		return nil, err
	}
	return s.getDecorated(ctx, userID, id, "")
}

func (s *Service) Restore(ctx context.Context, userID, id uuid.UUID) (*Account, error) {
	a, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if !a.CanEdit {
		return nil, ErrNotOwner
	}
	if err := s.store.Restore(ctx, userID, id); err != nil {
		return nil, err
	}
	return s.getDecorated(ctx, userID, id, "")
}

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
