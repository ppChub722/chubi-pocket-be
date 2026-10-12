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
	// Wired post-construction via WithNotificationService (wallet invites).
	notifs *notifications.Service
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

// ListPage is the wallets page read: the list plus its totals and the
// archived count (spec AccountList).
func (s *Service) ListPage(ctx context.Context, userID uuid.UUID, status, accType string) (*AccountList, error) {
	accs, err := s.List(ctx, userID, status, accType)
	if err != nil {
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

// buildTotals sums the wallets in the user's currency (other-currency legacy
// wallets are only counted). assets = balances of asset-group wallets;
// liabilities = what credit_card / pay_later wallets owe (a negative balance
// is debt); net = assets - liabilities.
func buildTotals(accs []Account, currency string) AccountTotals {
	t := AccountTotals{Currency: currency, ByType: []TypeTotal{}}
	byType := map[string]*TypeTotal{}
	for _, a := range accs {
		if a.IsShared {
			t.SharedCount++
		}
		if !a.TrackBalance {
			t.UntrackedCount++
			continue
		}
		if a.Currency != currency {
			t.OtherCurrencyCount++
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
	return t
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// GetDetail is the wallet page read: the wallet; include lists the optional
// blocks ("summary" for from..to, default the current month; "members" for
// the full membership history). Archived wallets work too.
func (s *Service) GetDetail(ctx context.Context, userID, id uuid.UUID, include []string, from, to string) (*AccountDetail, error) {
	wantSummary, wantMembers := false, false
	for _, inc := range include {
		switch strings.TrimSpace(inc) {
		case "summary":
			wantSummary = true
		case "members":
			wantMembers = true
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
	a, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	out := &AccountDetail{Account: *a}
	if wantSummary {
		if out.Summary, err = s.Summary(ctx, userID, id, from, to); err != nil {
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

// Summary computes income/expense/net for an account in a date range
// (default: the current month), filtered to **reportable** transactions only
// - categories with `include_in_report = false` (Opening Balance,
// Adjustment, Transfer In/Out, Lending, Reimbursements) are excluded.
// Uncategorized rows count (still real activity).
func (s *Service) Summary(ctx context.Context, userID, accountID uuid.UUID, from, to string) (*AccountSummary, error) {
	if _, err := s.store.GetByID(ctx, userID, accountID); err != nil {
		return nil, err
	}
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

// --- Create ---

func (s *Service) Create(ctx context.Context, userID uuid.UUID, req CreateRequest) (*Account, error) {
	if err := s.validateCreditFields(req.Type, req); err != nil {
		return nil, err
	}

	// A wallet takes the user's (locked) currency; the body has no say.
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

	// Owner auto-membership (spec §14/1.1): account_members exists for
	// EVERY account; a personal account is simply a single owner row with
	// report_scope='all' (fully counts in the owner's reports). Must land
	// before the opening-balance transaction — the transaction service
	// authorizes writes by active membership.
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
		// No description: the app labels it by its system category (owner 2026-10-10).
		_, newBalance, err := s.txs.CreateSystemInTx(ctx, tx, userID, created.ID, kind, amount, today, nil, nil)
		if err != nil {
			return nil, err
		}
		created.Balance = newBalance
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	// Re-fetch so the response carries members[] / my_report_scope /
	// is_shared (pinned §14 shape) — same re-fetch pattern as
	// projects.Create for members_count.
	return s.store.GetByID(ctx, userID, created.ID)
}

// --- Update ---

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*Account, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if !current.CanEdit {
		return nil, ErrNotOwner
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
	// Turned back on with the balance it has now: one adjustment row.
	if req.CurrentBalance != nil {
		if _, err := s.AdjustBalance(ctx, userID, id, AdjustBalanceRequest{NewBalance: req.CurrentBalance}); err != nil &&
			!errors.Is(err, ErrAdjustNoOp) {
			return nil, err
		}
	}
	// sort_order is the caller's own position (not a column of the wallet).
	if req.SortOrder != nil {
		if err := s.store.SetMySortOrder(ctx, userID, id, *req.SortOrder); err != nil {
			return nil, err
		}
	}
	// Re-fetch for the members[] / my_report_scope / is_shared shape.
	return s.store.GetByID(ctx, userID, id)
}

// AddIdentifier appends one identifier to the wallet's list (spec 15 §8,
// "remember this number" on the pending page); an identical one is not
// added twice. Owner only, like every wallet field.
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

// --- Adjust balance ---

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
	// Only what the user typed — no canned text; the app labels the row by
	// its system category (owner 2026-10-10).
	created, newBalance, err := s.txs.CreateSystemInTx(ctx, tx, userID, accountID, kind, amount, date, req.Description, req.Note)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	_ = newBalance

	// Final state: the wallet, the adjustment row as the transactions module
	// returns it, and the wallet's balance after the write.
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

// --- Archive ---

// Archive soft-deletes (status='archived'). Spec §14/2 rule 5: a wallet
// with other active members cannot be deleted or archived away — the
// owner must be the only active member.
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
	return s.store.GetByID(ctx, userID, id)
}

// Restore brings an archived wallet back (owner only); an active one is
// returned as is.
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
	return s.store.GetByID(ctx, userID, id)
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
