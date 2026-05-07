package saving_goals

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

// Create — validates ownership, computes default allocation if omitted
// (Phase 1c rule: fill remaining capacity, never mutate existing
// goals), enforces sum ≤ 100, and inserts atomically.
func (s *Service) Create(
	ctx context.Context, userID uuid.UUID, req CreateRequest,
) (*SavingGoalView, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Verify the linked account belongs to caller and is active.
	acc, err := s.store.VerifyAccountOwnership(ctx, tx, userID, req.LinkedAccountID)
	if err != nil {
		return nil, err
	}

	// 2. Lock active sibling rows + sum existing allocation on the account.
	existingSum, err := s.store.SumActiveAllocationOnAccountForUpdate(
		ctx, tx, req.LinkedAccountID, nil)
	if err != nil {
		return nil, err
	}

	// 3. Resolve allocation_pct.
	//    - User-supplied: must satisfy existingSum + value ≤ 100.
	//    - Default: fill remaining capacity (never silently mutate
	//      existing goals). If capacity is 0, reject — user must
	//      explicitly free up room first.
	var allocation float64
	if req.AllocationPct != nil {
		allocation = *req.AllocationPct
	} else {
		allocation = round2(100 - existingSum)
		if allocation <= 0 {
			return nil, ErrAllocationExceeded
		}
	}
	if existingSum+allocation > 100+1e-6 {
		return nil, ErrAllocationExceeded
	}

	// 4. Insert.
	row, err := s.store.CreateInTx(ctx, tx, userID, req, allocation)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	v := buildView(*row, AccountSnapshot{
		ID: acc.ID, Name: acc.Name, Balance: acc.Balance, Currency: acc.Currency,
	})
	return &v, nil
}

func (s *Service) Get(
	ctx context.Context, userID, id uuid.UUID,
) (*SavingGoalView, error) {
	g, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	acc, err := s.store.VerifyAccountOwnership(ctx, s.store.Pool(), userID, g.LinkedAccountID)
	if err != nil {
		// The linked account was archived but the goal lives on. Fall
		// back to a zero-balance snapshot so the response shape stays
		// consistent — this case is rare since accounts ON DELETE
		// RESTRICT prevents deleting an account with goals.
		if errors.Is(err, ErrAccountNotOwned) {
			acc = &AccountSnapshot{ID: g.LinkedAccountID, Name: "(archived)", Balance: 0, Currency: "THB"}
		} else {
			return nil, err
		}
	}
	v := buildView(*g, *acc)
	return &v, nil
}

func (s *Service) List(
	ctx context.Context, userID uuid.UUID, f ListFilter,
) (*ListResponse, error) {
	rows, err := s.store.List(ctx, userID, f)
	if err != nil {
		return nil, err
	}

	snapshots, err := s.store.ListAccountSnapshotsForGoals(ctx, userID, rows)
	if err != nil {
		return nil, err
	}

	out := make([]SavingGoalView, 0, len(rows))
	for _, g := range rows {
		acc, ok := snapshots[g.LinkedAccountID]
		if !ok {
			// Linked account went missing (e.g. archived). Fallback
			// snapshot keeps response shape stable.
			acc = AccountSnapshot{ID: g.LinkedAccountID, Name: "(archived)", Balance: 0, Currency: "THB"}
		}
		v := buildView(g, acc)

		// is_completed filter applied post-compute since it's derived.
		if f.IsCompleted != nil && v.IsCompleted != *f.IsCompleted {
			continue
		}
		out = append(out, v)
	}
	return &ListResponse{Data: out}, nil
}

// Update — partial patch. Allocation changes re-check the sum.
func (s *Service) Update(
	ctx context.Context, userID, id uuid.UUID, req UpdateRequest,
) (*SavingGoalView, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := s.store.GetByIDInTx(ctx, tx, userID, id)
	if err != nil {
		return nil, err
	}

	if req.AllocationPct != nil && current.Status == StatusActive {
		// Lock siblings excluding this goal, then verify the new
		// allocation fits. Archived goals are excluded from the sum
		// regardless via status='active' filter, but excluding by
		// id keeps the comparison correct when the row IS active.
		excl := id
		sumExcl, err := s.store.SumActiveAllocationOnAccountForUpdate(
			ctx, tx, current.LinkedAccountID, &excl)
		if err != nil {
			return nil, err
		}
		if sumExcl+*req.AllocationPct > 100+1e-6 {
			return nil, ErrAllocationExceeded
		}
	}

	updated, err := s.store.UpdateInTx(ctx, tx, userID, id, req)
	if err != nil {
		return nil, err
	}

	acc, err := s.store.VerifyAccountOwnership(ctx, tx, userID, updated.LinkedAccountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotOwned) {
			acc = &AccountSnapshot{ID: updated.LinkedAccountID, Name: "(archived)", Balance: 0, Currency: "THB"}
		} else {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	v := buildView(*updated, *acc)
	return &v, nil
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return s.store.Delete(ctx, userID, id)
}

// Archive — sets status='archived'. Frees allocation capacity on
// the linked account; idempotent.
func (s *Service) Archive(
	ctx context.Context, userID, id uuid.UUID,
) (*SavingGoalView, error) {
	return s.transitionStatus(ctx, userID, id, StatusArchived)
}

// Restore — sets status='active'. Re-checks that the goal's stored
// allocation_pct still fits the current account capacity. If another
// goal has already absorbed the slot, restore is rejected and the
// user must lower the allocation first via PUT.
func (s *Service) Restore(
	ctx context.Context, userID, id uuid.UUID,
) (*SavingGoalView, error) {
	return s.transitionStatus(ctx, userID, id, StatusActive)
}

func (s *Service) transitionStatus(
	ctx context.Context, userID, id uuid.UUID, target string,
) (*SavingGoalView, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := s.store.GetByIDInTx(ctx, tx, userID, id)
	if err != nil {
		return nil, err
	}

	if current.Status == target {
		// Idempotent: just return the current view.
		acc, accErr := s.store.VerifyAccountOwnership(ctx, tx, userID, current.LinkedAccountID)
		if accErr != nil {
			if errors.Is(accErr, ErrAccountNotOwned) {
				acc = &AccountSnapshot{ID: current.LinkedAccountID, Name: "(archived)", Balance: 0, Currency: "THB"}
			} else {
				return nil, accErr
			}
		}
		_ = tx.Commit(ctx)
		v := buildView(*current, *acc)
		return &v, nil
	}

	// Restore guard — re-check capacity using the current goal's
	// stored allocation. Other goals (excluding this one) must
	// leave room.
	if target == StatusActive {
		excl := id
		sumExcl, err := s.store.SumActiveAllocationOnAccountForUpdate(
			ctx, tx, current.LinkedAccountID, &excl)
		if err != nil {
			return nil, err
		}
		if sumExcl+current.AllocationPct > 100+1e-6 {
			return nil, ErrAllocationExceeded
		}
	}

	updated, err := s.store.SetStatusInTx(ctx, tx, userID, id, target)
	if err != nil {
		return nil, err
	}

	acc, err := s.store.VerifyAccountOwnership(ctx, tx, userID, updated.LinkedAccountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotOwned) {
			acc = &AccountSnapshot{ID: updated.LinkedAccountID, Name: "(archived)", Balance: 0, Currency: "THB"}
		} else {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	v := buildView(*updated, *acc)
	return &v, nil
}

// AccountAllocations — backs `GET /accounts/:id/saving-allocations`.
// Verifies ownership, lists all active goals on the account, computes
// allocated / unallocated split for the FE pie.
func (s *Service) AccountAllocations(
	ctx context.Context, userID, accountID uuid.UUID,
) (*AccountAllocationsResponse, error) {
	acc, err := s.store.VerifyAccountOwnership(ctx, s.store.Pool(), userID, accountID)
	if err != nil {
		return nil, err
	}

	rows, err := s.store.List(ctx, userID, ListFilter{
		Status:    StatusActive,
		AccountID: &accountID,
	})
	if err != nil {
		return nil, err
	}

	out := AccountAllocationsResponse{
		AccountID:      acc.ID,
		AccountBalance: acc.Balance,
		Currency:       acc.Currency,
		ActiveGoals:    make([]AccountAllocationGoal, 0, len(rows)),
	}

	var sumPct float64
	for _, g := range rows {
		current := round2(acc.Balance * g.AllocationPct / 100)
		out.ActiveGoals = append(out.ActiveGoals, AccountAllocationGoal{
			ID:            g.ID,
			Name:          g.Name,
			AllocationPct: g.AllocationPct,
			CurrentAmount: current,
		})
		sumPct += g.AllocationPct
	}
	out.AllocatedPct = round2(sumPct)
	out.UnallocatedPct = round2(100 - sumPct)
	out.UnallocatedAmount = round2(acc.Balance * out.UnallocatedPct / 100)
	return &out, nil
}

// --- helpers ---

func buildView(g SavingGoal, acc AccountSnapshot) SavingGoalView {
	current := round2(acc.Balance * g.AllocationPct / 100)
	progressPct := 0.0
	if g.TargetAmount > 0 {
		progressPct = round2(current / g.TargetAmount * 100)
	}
	remaining := g.TargetAmount - current
	if remaining < 0 {
		remaining = 0
	}

	v := SavingGoalView{
		SavingGoal: g,
		LinkedAccount: LinkedAccountSummary{
			ID: acc.ID, Name: acc.Name, Balance: acc.Balance, Currency: acc.Currency,
		},
		Currency:        acc.Currency,
		CurrentAmount:   current,
		ProgressPct:     progressPct,
		IsCompleted:     current >= g.TargetAmount,
		RemainingAmount: round2(remaining),
	}
	if g.Deadline != nil && *g.Deadline != "" {
		if d, err := time.Parse("2006-01-02", *g.Deadline); err == nil {
			days := int(time.Until(d).Hours() / 24)
			v.DaysRemaining = &days
		}
	}
	return v
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
