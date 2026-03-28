package budget

import (
	"context"
	"time"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

// =============================================================
// BUDGETS
// =============================================================

func (s *Service) CreateBudget(ctx context.Context, userID int64, req CreateBudgetRequest) (*Budget, error) {
	now := time.Now()
	b := &Budget{
		UserID:     userID,
		CategoryID: req.CategoryID,
		Amount:     req.Amount,
		Period:     req.Period,
		IsActive:   true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.store.CreateBudget(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Service) GetBudgets(ctx context.Context, userID int64, isActive *bool, period *string) ([]BudgetWithSpending, error) {
	return s.store.GetBudgets(ctx, userID, isActive, period)
}

func (s *Service) GetBudgetByID(ctx context.Context, userID, budgetID int64) (*BudgetDetail, error) {
	ownerID, err := s.store.GetBudgetOwnerID(ctx, budgetID)
	if err != nil {
		return nil, err
	}
	if ownerID != userID {
		return nil, ErrForbidden
	}
	return s.store.GetBudgetByID(ctx, budgetID)
}

func (s *Service) UpdateBudget(ctx context.Context, userID, budgetID int64, req UpdateBudgetRequest) error {
	ownerID, err := s.store.GetBudgetOwnerID(ctx, budgetID)
	if err != nil {
		return err
	}
	if ownerID != userID {
		return ErrForbidden
	}
	return s.store.UpdateBudget(ctx, budgetID, req)
}

func (s *Service) DeleteBudget(ctx context.Context, userID, budgetID int64) error {
	ownerID, err := s.store.GetBudgetOwnerID(ctx, budgetID)
	if err != nil {
		return err
	}
	if ownerID != userID {
		return ErrForbidden
	}
	return s.store.DeleteBudget(ctx, budgetID)
}

func (s *Service) GetBudgetOverview(ctx context.Context, userID int64, period string) (*BudgetOverview, error) {
	if period == "" {
		period = "monthly"
	}
	active := true
	budgets, err := s.store.GetBudgets(ctx, userID, &active, &period)
	if err != nil {
		return nil, err
	}

	start, end := getPeriodBounds(period)
	overview := &BudgetOverview{
		Period:      period,
		PeriodStart: start,
		PeriodEnd:   end,
		Budgets:     budgets,
	}

	for _, b := range budgets {
		overview.TotalBudget += b.Amount
		overview.TotalSpent += b.SpentThisPeriod
		if b.SpentThisPeriod > b.Amount {
			overview.BudgetsOverLimit++
		}
	}
	overview.TotalRemaining = overview.TotalBudget - overview.TotalSpent

	return overview, nil
}

// =============================================================
// SAVING GOALS
// =============================================================

func (s *Service) CreateSavingGoal(ctx context.Context, userID int64, req CreateSavingGoalRequest) (*SavingGoal, error) {
	now := time.Now()
	currentAmount := 0.0
	if req.CurrentAmount != nil {
		currentAmount = *req.CurrentAmount
	}
	sg := &SavingGoal{
		UserID:        userID,
		Name:          req.Name,
		TargetAmount:  req.TargetAmount,
		CurrentAmount: currentAmount,
		AccountID:     req.AccountID,
		Deadline:      req.Deadline,
		Icon:          req.Icon,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.store.CreateSavingGoal(ctx, sg); err != nil {
		return nil, err
	}
	if sg.TargetAmount > 0 {
		sg.ProgressPct = int((sg.CurrentAmount / sg.TargetAmount) * 100)
	}
	return sg, nil
}

func (s *Service) GetSavingGoals(ctx context.Context, userID int64, isCompleted *bool) ([]SavingGoal, error) {
	return s.store.GetSavingGoals(ctx, userID, isCompleted)
}

func (s *Service) GetSavingGoalByID(ctx context.Context, userID, goalID int64) (*SavingGoalDetail, error) {
	ownerID, err := s.store.GetSavingGoalOwnerID(ctx, goalID)
	if err != nil {
		return nil, err
	}
	if ownerID != userID {
		return nil, ErrForbidden
	}
	return s.store.GetSavingGoalByID(ctx, goalID)
}

func (s *Service) UpdateSavingGoal(ctx context.Context, userID, goalID int64, req UpdateSavingGoalRequest) error {
	ownerID, err := s.store.GetSavingGoalOwnerID(ctx, goalID)
	if err != nil {
		return err
	}
	if ownerID != userID {
		return ErrForbidden
	}
	return s.store.UpdateSavingGoal(ctx, goalID, req)
}

func (s *Service) DeleteSavingGoal(ctx context.Context, userID, goalID int64) error {
	ownerID, err := s.store.GetSavingGoalOwnerID(ctx, goalID)
	if err != nil {
		return err
	}
	if ownerID != userID {
		return ErrForbidden
	}
	return s.store.DeleteSavingGoal(ctx, goalID)
}
