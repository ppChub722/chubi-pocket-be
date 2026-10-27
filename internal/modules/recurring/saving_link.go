package recurring

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
)

const typeSaving = "saving"

// ErrSavingNeedsWallet — a saving setting (or occurrence) has no wallet to save from.
var ErrSavingNeedsWallet = errors.New("a saving needs a wallet")

func (s *Service) WithSavings(sv *saving_goals.Service) { s.savings = sv }

func checkSavingTx(ctx context.Context, q rowQuerier, userID uuid.UUID, account, goal *uuid.UUID) error {
	if account == nil {
		return ErrSavingNeedsWallet
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

// switchesSaving — an occurrence can't move between a saving and a money type.
func switchesSaving(before, after pending.Draft) bool {
	return (draftType(before) == typeSaving) != (draftType(after) == typeSaving)
}

// recordSavingTx records a saving occurrence: a deposit of its amount from its wallet into
// its goal (or the unallocated pot), dated when, linked to the row.
func (s *Service) recordSavingTx(ctx context.Context, tx pgx.Tx, row *itemRow, when time.Time, desc *string, auto bool) ([]uuid.UUID, any, error) {
	d := row.Draft
	if s.savings == nil {
		return nil, nil, ErrValidation
	}
	if err := checkSavingTx(ctx, tx, row.UserID, d.AccountID, d.GoalID); err != nil {
		return nil, nil, err
	}
	note := d.Note
	if note == nil || *note == "" {
		note = desc
	}
	id, err := s.savings.SaveFromDraftTx(ctx, tx, row.UserID, d.AccountID, d.GoalID, *d.Amount, when.Format(dateLayout),
		truncRunes(note, 500), saving_goals.SourceRecurring)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	row.SavingEntryID, row.ProducedAt = &id, &now
	row.Archived, row.ClearedAt = false, nil
	if auto {
		row.AutoCreatedAt = &now
	}
	if err := saveItemTx(ctx, tx, row); err != nil {
		return nil, nil, err
	}
	return []uuid.UUID{*d.AccountID}, nil, nil
}

func checkRuleRefsTx(ctx context.Context, q rowQuerier, userID uuid.UUID, r *Rule) error {
	if err := checkRefsTx(ctx, q, userID, r.Type, r.AccountID, r.ToAccountID, r.CategoryID, r.TagIDs); err != nil {
		return err
	}
	if r.Type == typeSaving {
		return checkSavingTx(ctx, q, userID, r.AccountID, r.GoalID)
	}
	return nil
}
