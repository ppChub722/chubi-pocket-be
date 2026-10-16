package recurring

// Recurring ออม (goals v2, 0.3.1-10): a setting of type saving saves
// `amount` from its wallet into a goal (nil = the spare bucket). Each item
// becomes a pending saving draft (🐷); confirming it makes a saving entry,
// linked to the item like a transaction would be.

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
)

const typeSaving = "saving"

func (s *Service) WithSavings(sv *saving_goals.Service) { s.savings = sv }

// checkSavingTx — a saving setting / item: the caller's active wallet that
// tracks its balance, and an active goal of theirs (nil = spare).
func checkSavingTx(ctx context.Context, q rowQuerier, userID uuid.UUID, account, goal *uuid.UUID) error {
	if account == nil {
		return ErrValidation
	}
	var tracked bool
	if err := q.QueryRow(ctx, `SELECT track_balance FROM accounts WHERE id = $1`, *account).Scan(&tracked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountNotFound
		}
		return err
	}
	if !tracked {
		return saving_goals.ErrWalletNotTracked
	}
	if goal == nil {
		return nil
	}
	var status string
	err := q.QueryRow(ctx, `SELECT status FROM saving_goals WHERE id = $1 AND user_id = $2`, *goal, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return saving_goals.ErrGoalNotFound
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return saving_goals.ErrGoalNotActive
	}
	return nil
}

// checkRuleRefsTx — a setting's references.
func checkRuleRefsTx(ctx context.Context, q rowQuerier, userID uuid.UUID, r *Rule) error {
	if err := checkRefsTx(ctx, q, userID, r.Type, r.AccountID, r.ToAccountID, r.CategoryID, r.TagIDs); err != nil {
		return err
	}
	if r.Type == typeSaving {
		return checkSavingTx(ctx, q, userID, r.AccountID, r.GoalID)
	}
	return nil
}

