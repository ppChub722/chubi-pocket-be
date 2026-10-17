package saving_goals

// Auto-saving (0.4.0): one setting per goal (and one for unallocated money,
// goal_id NULL): amount, day of month (31 = the last day), and the wallet it
// comes from or none. On the day the recurring due check makes a saving draft
// (🐷) for the month; the user confirms it like any pending draft.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// AutoSave — spec SavingAutoSave.
type AutoSave struct {
	Amount float64 `json:"amount"`
	// Day of the month the draft is made on (31 = the last day of a short month).
	Day      int                `json:"day"`
	Wallet   *shared.AccountRef `json:"wallet"` // null = no source wallet
	NextDate *string            `json:"next_date"`
}

// AutoSaveRequest — PUT /saving-goals/:id/auto-save (and /spare/auto-save).
type AutoSaveRequest struct {
	Amount    float64    `json:"amount"     binding:"required,gt=0"`
	Day       int        `json:"day"        binding:"required,min=1,max=31"`
	AccountID *uuid.UUID `json:"account_id"`
}

type autoRow struct {
	ID        uuid.UUID
	Amount    float64
	Day       int
	AccountID *uuid.UUID
	LastMonth *time.Time
}

func (s *Store) getAuto(ctx context.Context, q querier, userID uuid.UUID, goalID *uuid.UUID) (*autoRow, error) {
	var r autoRow
	err := q.QueryRow(ctx, `SELECT id, amount::float8, day, account_id, last_month FROM saving_auto
		WHERE user_id = $1 AND goal_id IS NOT DISTINCT FROM $2`, userID, goalID).Scan(&r.ID, &r.Amount, &r.Day, &r.AccountID, &r.LastMonth)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("auto-save: %w", err)
	}
	return &r, nil
}

// dayInMonth — `day` clamped to the month's length.
func dayInMonth(first time.Time, day int) time.Time {
	last := first.AddDate(0, 1, -1).Day()
	if day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}

// nextAutoDate — when the next draft is made: this month's day if its draft is
// still to come, else next month's.
func nextAutoDate(r *autoRow, today time.Time) time.Time {
	first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	made := r.LastMonth != nil && !r.LastMonth.Before(first)
	d := dayInMonth(first, r.Day)
	if made || d.Before(today) {
		d = dayInMonth(first.AddDate(0, 1, 0), r.Day)
	}
	return d
}

// autoViews — the auto-save of each goal (key uuid.Nil = unallocated) as views.
func (s *Service) autoViews(ctx context.Context, userID uuid.UUID, today time.Time) (map[uuid.UUID]*AutoSave, error) {
	rows, err := s.store.db.Query(ctx, `SELECT COALESCE(goal_id, '00000000-0000-0000-0000-000000000000'::uuid),
		amount::float8, day, account_id, last_month FROM saving_auto WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("auto-saves: %w", err)
	}
	type rec struct {
		key uuid.UUID
		r   autoRow
	}
	var recs []rec
	var accIDs []uuid.UUID
	for rows.Next() {
		var x rec
		if err := rows.Scan(&x.key, &x.r.Amount, &x.r.Day, &x.r.AccountID, &x.r.LastMonth); err != nil {
			rows.Close()
			return nil, err
		}
		if x.r.AccountID != nil {
			accIDs = append(accIDs, *x.r.AccountID)
		}
		recs = append(recs, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	accs, err := shared.LoadAccountRefs(ctx, s.store.db, accIDs)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]*AutoSave{}
	for _, x := range recs {
		v := &AutoSave{Amount: round2(x.r.Amount), Day: x.r.Day}
		if x.r.AccountID != nil {
			if a, ok := accs[*x.r.AccountID]; ok {
				v.Wallet = &a
			}
		}
		nd := nextAutoDate(&x.r, today)
		v.NextDate = dstr(&nd)
		out[x.key] = v
	}
	return out, nil
}

// SetAutoSave — PUT: create or replace the setting. goalID nil = unallocated.
func (s *Service) SetAutoSave(ctx context.Context, userID uuid.UUID, goalID *uuid.UUID, r AutoSaveRequest) error {
	if r.Amount <= 0 || r.Day < 1 || r.Day > 31 {
		return ErrValidation
	}
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if goalID != nil {
		if err := s.checkGoalTarget(ctx, tx, userID, goalID); err != nil {
			return err
		}
	}
	if r.AccountID != nil {
		if err := checkWallet(ctx, tx, userID, *r.AccountID); err != nil {
			return err
		}
	}
	id, _ := uuid.NewV7()
	// A changed setting makes this month's draft again only if it was not made yet.
	if _, err := tx.Exec(ctx, `INSERT INTO saving_auto (id, user_id, goal_id, amount, day, account_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, goal_id) DO UPDATE SET amount = EXCLUDED.amount, day = EXCLUDED.day,
			account_id = EXCLUDED.account_id`,
		id, userID, goalID, round2(r.Amount), r.Day, r.AccountID); err != nil {
		return fmt.Errorf("set auto-save: %w", err)
	}
	return tx.Commit(ctx)
}

// ClearAutoSave — DELETE: stop auto-saving (idempotent).
func (s *Service) ClearAutoSave(ctx context.Context, userID uuid.UUID, goalID *uuid.UUID) error {
	_, err := s.store.db.Exec(ctx, `DELETE FROM saving_auto WHERE user_id = $1 AND goal_id IS NOT DISTINCT FROM $2`, userID, goalID)
	return err
}

// AutoSaveGoal / AutoSaveSpare — the PUT / DELETE answers: the goal page /
// the spare page after the change.
func (s *Service) PutAutoSaveGoal(ctx context.Context, userID, goalID uuid.UUID, r AutoSaveRequest) (*GoalDetail, error) {
	if err := s.SetAutoSave(ctx, userID, &goalID, r); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, goalID, entriesPerPage, false)
}

func (s *Service) DeleteAutoSaveGoal(ctx context.Context, userID, goalID uuid.UUID) (*GoalDetail, error) {
	if _, err := s.store.getGoal(ctx, s.store.db, userID, goalID, false); err != nil {
		return nil, err
	}
	if err := s.ClearAutoSave(ctx, userID, &goalID); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, goalID, entriesPerPage, false)
}

func (s *Service) PutAutoSaveSpare(ctx context.Context, userID uuid.UUID, r AutoSaveRequest) (*SpareDetail, error) {
	if err := s.SetAutoSave(ctx, userID, nil, r); err != nil {
		return nil, err
	}
	return s.Spare(ctx, userID, entriesPerPage)
}

func (s *Service) DeleteAutoSaveSpare(ctx context.Context, userID uuid.UUID) (*SpareDetail, error) {
	if err := s.ClearAutoSave(ctx, userID, nil); err != nil {
		return nil, err
	}
	return s.Spare(ctx, userID, entriesPerPage)
}
