package shared

import (
	"context"
	"errors"
	"math"
	"time"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) CreateSharedExpense(ctx context.Context, userID int64, req CreateSharedExpenseRequest) (*SharedExpenseWithSplits, error) {
	// Validate splits sum
	var splitsTotal float64
	for _, sp := range req.Splits {
		splitsTotal += sp.OwedAmount
	}
	if math.Abs(splitsTotal-req.TotalAmount) > 0.01 {
		return nil, errors.New("splits must sum to total_amount")
	}

	// Check if already linked
	linked, err := s.store.IsTransactionLinked(ctx, req.TransactionID)
	if err != nil {
		return nil, err
	}
	if linked {
		return nil, ErrAlreadyLinked
	}

	se := &SharedExpense{
		TransactionID: req.TransactionID,
		TotalAmount:   req.TotalAmount,
		Description:   req.Description,
		CreatedAt:     time.Now(),
	}
	if err := s.store.CreateSharedExpense(ctx, se); err != nil {
		return nil, err
	}

	// Create splits
	now := time.Now()
	var splits []Split
	for _, sp := range req.Splits {
		split := &Split{
			SharedExpenseID: se.ID,
			PersonName:      sp.PersonName,
			OwedAmount:      sp.OwedAmount,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := s.store.CreateSplit(ctx, split); err != nil {
			return nil, err
		}
		split.Outstanding = split.OwedAmount
		splits = append(splits, *split)
	}

	return &SharedExpenseWithSplits{
		SharedExpense:    *se,
		Splits:           splits,
		TotalOutstanding: req.TotalAmount,
	}, nil
}

func (s *Service) GetSharedExpenses(ctx context.Context, userID int64, isSettled *bool, from, to *string, page, perPage int) ([]SharedExpenseListItem, int, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	return s.store.GetSharedExpenses(ctx, userID, isSettled, from, to, page, perPage)
}

func (s *Service) GetSharedExpenseByID(ctx context.Context, userID, seID int64) (*SharedExpenseWithSplits, error) {
	ownerID, err := s.store.GetSharedExpenseOwnerID(ctx, seID)
	if err != nil {
		return nil, err
	}
	if ownerID != userID {
		return nil, errors.New("forbidden")
	}
	return s.store.GetSharedExpenseByID(ctx, seID)
}

func (s *Service) UpdateSharedExpense(ctx context.Context, userID, seID int64, req UpdateSharedExpenseRequest) error {
	ownerID, err := s.store.GetSharedExpenseOwnerID(ctx, seID)
	if err != nil {
		return err
	}
	if ownerID != userID {
		return errors.New("forbidden")
	}
	return s.store.UpdateSharedExpense(ctx, seID, req.Description)
}

func (s *Service) DeleteSharedExpense(ctx context.Context, userID, seID int64) error {
	ownerID, err := s.store.GetSharedExpenseOwnerID(ctx, seID)
	if err != nil {
		return err
	}
	if ownerID != userID {
		return errors.New("forbidden")
	}
	return s.store.DeleteSharedExpense(ctx, seID)
}

func (s *Service) GetSplits(ctx context.Context, userID, seID int64) ([]Split, error) {
	ownerID, err := s.store.GetSharedExpenseOwnerID(ctx, seID)
	if err != nil {
		return nil, err
	}
	if ownerID != userID {
		return nil, errors.New("forbidden")
	}
	return s.store.GetSplitsByExpenseID(ctx, seID)
}

func (s *Service) AddSplit(ctx context.Context, userID, seID int64, req CreateSplitRequest) (*Split, error) {
	ownerID, err := s.store.GetSharedExpenseOwnerID(ctx, seID)
	if err != nil {
		return nil, err
	}
	if ownerID != userID {
		return nil, errors.New("forbidden")
	}
	now := time.Now()
	split := &Split{
		SharedExpenseID: seID,
		PersonName:      req.PersonName,
		OwedAmount:      req.OwedAmount,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.store.CreateSplit(ctx, split); err != nil {
		return nil, err
	}
	split.Outstanding = split.OwedAmount
	return split, nil
}

func (s *Service) UpdateSplit(ctx context.Context, userID, splitID int64, req UpdateSplitRequest) (*Split, error) {
	ownerID, err := s.store.GetSplitOwnerID(ctx, splitID)
	if err != nil {
		return nil, err
	}
	if ownerID != userID {
		return nil, errors.New("forbidden")
	}
	// Validate: can't reduce below paid
	if req.OwedAmount != nil {
		split, err := s.store.GetSplitByID(ctx, splitID)
		if err != nil {
			return nil, err
		}
		if *req.OwedAmount < split.PaidAmount {
			return nil, errors.New("cannot reduce owed_amount below paid_amount")
		}
	}
	return s.store.UpdateSplit(ctx, splitID, req)
}

func (s *Service) DeleteSplit(ctx context.Context, userID, splitID int64) error {
	ownerID, err := s.store.GetSplitOwnerID(ctx, splitID)
	if err != nil {
		return err
	}
	if ownerID != userID {
		return errors.New("forbidden")
	}
	return s.store.DeleteSplit(ctx, splitID)
}

func (s *Service) GetSettlements(ctx context.Context, userID, splitID int64) ([]Settlement, error) {
	ownerID, err := s.store.GetSplitOwnerID(ctx, splitID)
	if err != nil {
		return nil, err
	}
	if ownerID != userID {
		return nil, errors.New("forbidden")
	}
	return s.store.GetSettlements(ctx, splitID)
}

func (s *Service) CreateSettlement(ctx context.Context, userID, splitID int64, req CreateSettlementRequest) (*Settlement, *Split, error) {
	ownerID, err := s.store.GetSplitOwnerID(ctx, splitID)
	if err != nil {
		return nil, nil, err
	}
	if ownerID != userID {
		return nil, nil, errors.New("forbidden")
	}

	settlement := &Settlement{
		SplitID:     splitID,
		Amount:      req.Amount,
		SettledDate: req.SettledDate,
		Note:        req.Note,
		CreatedAt:   time.Now(),
	}

	updatedSplit, err := s.store.CreateSettlement(ctx, settlement)
	if err != nil {
		return nil, nil, err
	}

	return settlement, updatedSplit, nil
}

func (s *Service) DeleteSettlement(ctx context.Context, userID, settlementID int64) error {
	ownerID, err := s.store.GetSettlementOwnerID(ctx, settlementID)
	if err != nil {
		return err
	}
	if ownerID != userID {
		return errors.New("forbidden")
	}
	return s.store.DeleteSettlement(ctx, settlementID)
}

func (s *Service) GetSummary(ctx context.Context, userID int64) (*SharedExpenseSummary, error) {
	return s.store.GetSummary(ctx, userID)
}
