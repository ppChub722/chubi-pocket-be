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
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
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

// AutoSaves — saving_goals.AutoSaveLister: the active saving settings into
// a goal (nil = spare).
func (s *Service) AutoSaves(ctx context.Context, userID uuid.UUID, goalID *uuid.UUID) ([]saving_goals.AutoSave, error) {
	cond := `goal_id IS NULL`
	args := []any{userID}
	if goalID != nil {
		cond, args = `goal_id = $2`, append(args, *goalID)
	}
	rows, err := s.db.Query(ctx, `SELECT `+ruleColumns+` FROM recurring_rules
		WHERE user_id = $1 AND type = 'saving' AND status = 'active' AND `+cond+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	var rules []*Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		rules = append(rules, r)
	}
	rows.Close()
	out := []saving_goals.AutoSave{}
	for _, r := range rules {
		var next *string
		var d *string
		if err := s.db.QueryRow(ctx, `SELECT to_char(MIN(i.date), 'YYYY-MM-DD') FROM recurring_items i
			WHERE i.rule_id = $1 AND `+statusSQL+` = 'upcoming'`, r.ID).Scan(&d); err != nil {
			return nil, err
		}
		next = d
		out = append(out, saving_goals.AutoSave{
			Recurring: shared.RecurringRef{ID: r.ID, Name: r.Name, IconCode: r.IconCode},
			Amount:    r.Amount, Rule: r.apiRule(), NextDate: next,
		})
	}
	return out, nil
}
