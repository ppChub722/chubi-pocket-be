package recurring

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

func (s *Service) CreateRecurring(ctx context.Context, userID int64, req CreateRecurringRequest) (*Recurring, error) {
	if req.EntryType == "installment" {
		if req.TotalAmount == nil || req.TotalInstallments == nil {
			return nil, errors.New("total_amount and total_installments are required for installments")
		}
	}

	now := time.Now()
	r := &Recurring{
		UserID:          userID,
		AccountID:       req.AccountID,
		Name:            req.Name,
		Type:            req.Type,
		EntryType:       req.EntryType,
		Amount:          req.Amount,
		CategoryID:      req.CategoryID,
		BillingCycle:    req.BillingCycle,
		NextBillingDate: req.NextBillingDate,
		Status:          "active",
		TotalAmount:     req.TotalAmount,
		DownPayment:     req.DownPayment,
		MonthlyPayment:  req.MonthlyPayment,
		TotalInstallments:     req.TotalInstallments,
		RemainingInstallments: req.TotalInstallments, // starts equal to total
		Note:            req.Note,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.store.CreateRecurring(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Service) GetRecurrings(ctx context.Context, userID int64, entryType, status, txType, billingCycle *string, page, perPage int) ([]RecurringListItem, int, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	return s.store.GetRecurrings(ctx, userID, entryType, status, txType, billingCycle, page, perPage)
}

func (s *Service) GetRecurringByID(ctx context.Context, userID, recurringID int64) (*Recurring, error) {
	r, err := s.store.GetRecurringByID(ctx, recurringID)
	if err != nil {
		return nil, err
	}
	if r.UserID != userID {
		return nil, ErrForbidden
	}
	return r, nil
}

func (s *Service) UpdateRecurring(ctx context.Context, userID, recurringID int64, req UpdateRecurringRequest) error {
	r, err := s.store.GetRecurringByID(ctx, recurringID)
	if err != nil {
		return err
	}
	if r.UserID != userID {
		return ErrForbidden
	}
	if r.Status == "completed" {
		return errors.New("cannot edit completed entry")
	}
	return s.store.UpdateRecurring(ctx, recurringID, req)
}

func (s *Service) DeleteRecurring(ctx context.Context, userID, recurringID int64) error {
	r, err := s.store.GetRecurringByID(ctx, recurringID)
	if err != nil {
		return err
	}
	if r.UserID != userID {
		return ErrForbidden
	}
	return s.store.DeleteRecurring(ctx, recurringID)
}

func (s *Service) Pause(ctx context.Context, userID, recurringID int64) error {
	r, err := s.store.GetRecurringByID(ctx, recurringID)
	if err != nil {
		return err
	}
	if r.UserID != userID {
		return ErrForbidden
	}
	if r.Status == "paused" {
		return errors.New("already paused")
	}
	if r.Status == "completed" {
		return errors.New("cannot pause completed entry")
	}
	return s.store.UpdateStatus(ctx, recurringID, "paused")
}

func (s *Service) Resume(ctx context.Context, userID, recurringID int64) error {
	r, err := s.store.GetRecurringByID(ctx, recurringID)
	if err != nil {
		return err
	}
	if r.UserID != userID {
		return ErrForbidden
	}
	if r.Status != "paused" {
		return errors.New("not paused")
	}
	return s.store.UpdateStatus(ctx, recurringID, "active")
}

func (s *Service) Cancel(ctx context.Context, userID, recurringID int64) error {
	r, err := s.store.GetRecurringByID(ctx, recurringID)
	if err != nil {
		return err
	}
	if r.UserID != userID {
		return ErrForbidden
	}
	if r.Status == "cancelled" {
		return errors.New("already cancelled")
	}
	return s.store.UpdateStatus(ctx, recurringID, "cancelled")
}

func (s *Service) GetUpcoming(ctx context.Context, userID int64, days int) ([]UpcomingItem, float64, error) {
	if days < 1 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	return s.store.GetUpcoming(ctx, userID, days)
}

func (s *Service) ProcessDueRecurrings(ctx context.Context) (int, error) {
	return s.store.ProcessDueRecurrings(ctx)
}
