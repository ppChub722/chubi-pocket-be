package scheduled_transactions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const dateLayout = "2006-01-02"

var (
	ErrInstallmentFieldsRequired = errors.New("installment requires total_amount, total_installments, remaining_installments")
	ErrRecurringExtraFields      = errors.New("recurring entry must not include installment-only fields")
	ErrRemainingExceedsTotal     = errors.New("remaining_installments cannot exceed total_installments")
	ErrInvalidTransition         = errors.New("invalid status transition")
	ErrTerminalStatus            = errors.New("schedule is in a terminal status")
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

// Create — validates the request, computes day_of_month from the
// supplied next_billing_date, defers ownership/category checks to
// the store helpers.
func (s *Service) Create(
	ctx context.Context, userID uuid.UUID, req CreateRequest,
) (*ScheduledTransactionView, error) {
	if err := validateCreate(req); err != nil {
		return nil, err
	}
	if err := s.store.VerifyAccountOwnership(ctx, userID, req.AccountID); err != nil {
		return nil, err
	}
	// CategoryID is now required on create — VerifyCategory still accepts
	// a nullable pointer for the Update path, so pass the address here.
	if err := s.store.VerifyCategory(ctx, userID, &req.CategoryID, req.Type); err != nil {
		return nil, err
	}

	dom, err := dayOfMonthFrom(req.NextBillingDate)
	if err != nil {
		return nil, err
	}

	row, err := s.store.Create(ctx, userID, req, dom)
	if err != nil {
		return nil, err
	}
	return s.hydrateOne(ctx, userID, row)
}

func (s *Service) Get(
	ctx context.Context, userID, id uuid.UUID,
) (*ScheduledTransactionView, error) {
	row, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return s.hydrateOne(ctx, userID, row)
}

func (s *Service) List(
	ctx context.Context, userID uuid.UUID, f ListFilter,
) (*ListResponse, error) {
	rows, err := s.store.List(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	views, err := s.hydrate(ctx, userID, rows)
	if err != nil {
		return nil, err
	}
	return &ListResponse{Data: views}, nil
}

func (s *Service) Upcoming(
	ctx context.Context, userID uuid.UUID, days int,
) (*UpcomingResponse, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	rows, err := s.store.Upcoming(ctx, userID, days)
	if err != nil {
		return nil, err
	}

	out := UpcomingResponse{Data: make([]UpcomingItem, 0, len(rows))}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, r := range rows {
		due, err := time.Parse(dateLayout, r.NextBillingDate)
		if err != nil {
			continue
		}
		daysUntil := int(due.Sub(today).Hours() / 24)
		if daysUntil < 0 {
			daysUntil = 0
		}
		out.Data = append(out.Data, UpcomingItem{
			ID:              r.ID,
			Name:            r.Name,
			Type:            r.Type,
			NextBillingDate: r.NextBillingDate,
			Amount:          r.Amount,
			DaysUntil:       daysUntil,
		})
		switch r.Type {
		case TypeExpense:
			out.TotalExpenseDue += r.Amount
		case TypeIncome:
			out.TotalIncomeDue += r.Amount
		}
	}
	return &out, nil
}

func (s *Service) Update(
	ctx context.Context, userID, id uuid.UUID, req UpdateRequest,
) (*ScheduledTransactionView, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	// Total/remaining sanity check using effective values.
	effTotal := current.TotalInstallments
	if req.TotalInstallments != nil {
		effTotal = req.TotalInstallments
	}
	effRem := current.RemainingInstallments
	if req.RemainingInstallments != nil {
		effRem = req.RemainingInstallments
	}
	if effTotal != nil && effRem != nil && *effRem > *effTotal {
		return nil, ErrRemainingExceedsTotal
	}

	if req.AccountID != nil {
		if err := s.store.VerifyAccountOwnership(ctx, userID, *req.AccountID); err != nil {
			return nil, err
		}
	}
	if req.CategoryID != nil {
		if err := s.store.VerifyCategory(ctx, userID, req.CategoryID, current.Type); err != nil {
			return nil, err
		}
	}

	var newDOM *int
	if req.NextBillingDate != nil {
		dom, err := dayOfMonthFrom(*req.NextBillingDate)
		if err != nil {
			return nil, err
		}
		newDOM = &dom
	}

	row, err := s.store.Update(ctx, userID, id, req, newDOM)
	if err != nil {
		return nil, err
	}
	return s.hydrateOne(ctx, userID, row)
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return s.store.Delete(ctx, userID, id)
}

// Pause / Resume / Cancel — explicit transitions, no free PUT status.
//
//   active   → paused, cancelled
//   paused   → active, cancelled
//   completed/cancelled → (terminal)
func (s *Service) Pause(
	ctx context.Context, userID, id uuid.UUID,
) (*ScheduledTransactionView, error) {
	return s.transition(ctx, userID, id, StatusActive, StatusPaused)
}

func (s *Service) Resume(
	ctx context.Context, userID, id uuid.UUID,
) (*ScheduledTransactionView, error) {
	return s.transition(ctx, userID, id, StatusPaused, StatusActive)
}

func (s *Service) Cancel(
	ctx context.Context, userID, id uuid.UUID,
) (*ScheduledTransactionView, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if current.Status == StatusCompleted || current.Status == StatusCancelled {
		return nil, ErrTerminalStatus
	}
	row, err := s.store.SetStatus(ctx, userID, id, StatusCancelled)
	if err != nil {
		return nil, err
	}
	return s.hydrateOne(ctx, userID, row)
}

func (s *Service) transition(
	ctx context.Context, userID, id uuid.UUID, from, to string,
) (*ScheduledTransactionView, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if current.Status == to {
		// Idempotent — return current view.
		return s.hydrateOne(ctx, userID, current)
	}
	if current.Status != from {
		return nil, ErrInvalidTransition
	}
	row, err := s.store.SetStatus(ctx, userID, id, to)
	if err != nil {
		return nil, err
	}
	return s.hydrateOne(ctx, userID, row)
}

// GenerateNow — runs one cycle of the scheduler for this entry. All
// effects land in a single DB tx: insert generated transactions row,
// adjust accounts.balance, advance next_billing_date, decrement
// remaining_installments (if installment), auto-complete if remaining
// hits zero.
//
// Available regardless of next_billing_date — Phase 1c dev/dogfooding.
func (s *Service) GenerateNow(
	ctx context.Context, userID, id uuid.UUID,
) (*GenerateNowResponse, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	sched, err := s.store.GetByIDForUpdate(ctx, tx, userID, id)
	if err != nil {
		return nil, err
	}
	if sched.Status != StatusActive {
		return nil, ErrInvalidTransition
	}

	if err := s.store.LockAccountTx(ctx, tx, sched.AccountID); err != nil {
		return nil, err
	}

	// Use the schedule's stored next_billing_date as the generated
	// transaction's date — matches what the real scheduler will do.
	txDate := sched.NextBillingDate
	delta := signedDelta(sched.Type, sched.Amount)

	if _, err := s.store.ApplyBalanceDeltaTx(ctx, tx, userID, sched.AccountID, delta); err != nil {
		return nil, err
	}

	txID, err := s.store.InsertGeneratedTxTx(ctx, tx, userID, sched.AccountID,
		sched.CategoryID, sched.Type, sched.Amount, txDate, sched.Note, id)
	if err != nil {
		return nil, err
	}

	// Advance next_billing_date.
	advanced, err := advanceDate(txDate, sched.BillingCycle, sched.DayOfMonth)
	if err != nil {
		return nil, err
	}

	decrement := sched.EntryType == EntryInstallment
	updated, err := s.store.AdvanceTx(ctx, tx, userID, id, advanced, decrement)
	if err != nil {
		return nil, err
	}

	// Auto-complete on zero remaining installments.
	finalStatus := updated.Status
	if updated.EntryType == EntryInstallment &&
		updated.RemainingInstallments != nil &&
		*updated.RemainingInstallments == 0 {
		if err := s.store.SetStatusInTx(ctx, tx, userID, id, StatusCompleted); err != nil {
			return nil, err
		}
		finalStatus = StatusCompleted
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &GenerateNowResponse{
		GeneratedTransaction: GeneratedTransaction{
			ID:     txID,
			Amount: sched.Amount,
			Date:   txDate,
		},
		ScheduleUpdated: ScheduleUpdated{
			NextBillingDate:       advanced,
			RemainingInstallments: updated.RemainingInstallments,
			Status:                finalStatus,
		},
	}, nil
}

func (s *Service) History(
	ctx context.Context, userID, id uuid.UUID,
) (*HistoryResponse, error) {
	// Ownership check via GetByID (errors if not owned).
	if _, err := s.store.GetByID(ctx, userID, id); err != nil {
		return nil, err
	}
	items, total, err := s.store.History(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return &HistoryResponse{
		Data:                 items,
		TotalGenerated:       len(items),
		TotalAmountGenerated: total,
	}, nil
}

// --- helpers ---

func validateCreate(req CreateRequest) error {
	if req.EntryType == EntryRecurring {
		// Recurring rejects installment-only fields.
		if req.TotalAmount != nil || req.DownPayment != nil ||
			req.TotalInstallments != nil || req.RemainingInstallments != nil ||
			req.InterestRate != nil {
			return ErrRecurringExtraFields
		}
		return nil
	}
	// Installment requires total_amount + total_installments + remaining.
	if req.TotalAmount == nil || req.TotalInstallments == nil || req.RemainingInstallments == nil {
		return ErrInstallmentFieldsRequired
	}
	if *req.RemainingInstallments > *req.TotalInstallments {
		return ErrRemainingExceedsTotal
	}
	return nil
}

func signedDelta(txType string, amount float64) float64 {
	if txType == TypeExpense {
		return -amount
	}
	return amount
}

func dayOfMonthFrom(date string) (int, error) {
	t, err := time.Parse(dateLayout, date)
	if err != nil {
		return 0, fmt.Errorf("parse date: %w", err)
	}
	return t.Day(), nil
}

// advanceDate returns next_billing_date after one cycle. For monthly,
// uses min(day_of_month, days_in_target_month) to preserve "last day"
// intent (spec §4.4). For yearly, same clamp on the new year/month.
func advanceDate(current, cycle string, dayOfMonth int) (string, error) {
	t, err := time.Parse(dateLayout, current)
	if err != nil {
		return "", fmt.Errorf("parse next_billing_date: %w", err)
	}
	switch cycle {
	case CycleDaily:
		return t.AddDate(0, 0, 1).Format(dateLayout), nil
	case CycleWeekly:
		return t.AddDate(0, 0, 7).Format(dateLayout), nil
	case CycleMonthly:
		next := t.AddDate(0, 1, 0)
		// AddDate normalizes Feb 31 → Mar 3; we want Feb 28/29 instead.
		// Re-anchor to first-of-month, then clamp the day.
		base := time.Date(next.Year(), next.Month(), 1, 0, 0, 0, 0, t.Location())
		days := daysInMonth(base.Year(), base.Month())
		day := dayOfMonth
		if day > days {
			day = days
		}
		return time.Date(base.Year(), base.Month(), day, 0, 0, 0, 0, t.Location()).Format(dateLayout), nil
	case CycleYearly:
		next := t.AddDate(1, 0, 0)
		base := time.Date(next.Year(), next.Month(), 1, 0, 0, 0, 0, t.Location())
		days := daysInMonth(base.Year(), base.Month())
		day := dayOfMonth
		if day > days {
			day = days
		}
		return time.Date(base.Year(), base.Month(), day, 0, 0, 0, 0, t.Location()).Format(dateLayout), nil
	}
	return "", fmt.Errorf("unknown billing_cycle: %s", cycle)
}

func daysInMonth(year int, month time.Month) int {
	// First day of next month, minus one day → last day of `month`.
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func (s *Service) hydrateOne(
	ctx context.Context, userID uuid.UUID, row *ScheduledTransaction,
) (*ScheduledTransactionView, error) {
	views, err := s.hydrate(ctx, userID, []ScheduledTransaction{*row})
	if err != nil {
		return nil, err
	}
	if len(views) == 0 {
		// Should never happen — hydrate emits one view per row.
		return &ScheduledTransactionView{ScheduledTransaction: *row}, nil
	}
	return &views[0], nil
}

func (s *Service) hydrate(
	ctx context.Context, userID uuid.UUID, rows []ScheduledTransaction,
) ([]ScheduledTransactionView, error) {
	if len(rows) == 0 {
		return []ScheduledTransactionView{}, nil
	}
	accountIDs := make([]uuid.UUID, 0, len(rows))
	categoryIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		accountIDs = append(accountIDs, r.AccountID)
		if r.CategoryID != nil {
			categoryIDs = append(categoryIDs, *r.CategoryID)
		}
	}
	accSnaps, err := s.store.AccountSnapshots(ctx, userID, accountIDs)
	if err != nil {
		return nil, err
	}
	catSnaps, err := s.store.CategorySnapshots(ctx, userID, categoryIDs)
	if err != nil {
		return nil, err
	}
	out := make([]ScheduledTransactionView, 0, len(rows))
	for _, r := range rows {
		v := ScheduledTransactionView{ScheduledTransaction: r}
		if a, ok := accSnaps[r.AccountID]; ok {
			v.Account = &AccountRef{ID: a.ID, Name: a.Name}
		}
		if r.CategoryID != nil {
			if c, ok := catSnaps[*r.CategoryID]; ok {
				v.Category = &CategoryRef{ID: c.ID, Name: c.Name}
			}
		}
		out = append(out, v)
	}
	return out, nil
}
