package saving_goals

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
	"github.com/ppChub722/chubi-pocket-be/internal/shared/recurrence"
)

const maxAutoSaves = 20

// AutoSave is the legacy single auto-save: the first of the goal's items in
// the old shape (old apps read it).
type AutoSave struct {
	Amount   float64            `json:"amount"`
	Day      int                `json:"day"`
	Wallet   *shared.AccountRef `json:"wallet"` // null = no source wallet
	NextDate *string            `json:"next_date"`
}

// AutoSaveItem is one auto-save: an amount from a wallet on a recurrence rule.
type AutoSaveItem struct {
	ID        uuid.UUID                  `json:"id"`
	Amount    float64                    `json:"amount"`
	Wallet    *shared.AccountRef         `json:"wallet"`
	Rule      recurrence.RecurrenceRule  `json:"rule"`
	StartDate string                     `json:"start_date"`
	End       recurrence.RecurrenceEnd   `json:"end"`
	NextDate  *string                    `json:"next_date"` // null = it has ended
}

type AutoSaveRequest struct {
	Amount    float64    `json:"amount"     binding:"required,gt=0"`
	Day       int        `json:"day"        binding:"required,min=1,max=31"`
	AccountID *uuid.UUID `json:"account_id"`
}

type AutoSaveInput struct {
	ID        *uuid.UUID                 `json:"id"`
	Amount    float64                    `json:"amount"`
	AccountID *uuid.UUID                 `json:"account_id"`
	Rule      *recurrence.RecurrenceRule `json:"rule"`
	StartDate string                     `json:"start_date"`
	End       *recurrence.RecurrenceEnd  `json:"end"`
}

type AutoSavesRequest struct {
	Items []AutoSaveInput `json:"items"`
}

// autoSet is what a goal (or the unallocated pot) shows of its auto-saves.
type autoSet struct {
	items     []AutoSaveItem
	legacy    *AutoSave
	monthPlan float64
}

type autoRow struct {
	ID           uuid.UUID
	GoalKey      uuid.UUID
	Amount       float64
	AccountID    *uuid.UUID
	Rule         []byte
	StartDate    time.Time
	End          []byte
	LastProduced *time.Time
}

func (r *autoRow) spec(today time.Time) (*recurrence.Spec, error) {
	var rule recurrence.RecurrenceRule
	var end recurrence.RecurrenceEnd
	if err := json.Unmarshal(r.Rule, &rule); err != nil {
		return nil, fmt.Errorf("auto-save rule: %w", err)
	}
	if err := json.Unmarshal(r.End, &end); err != nil {
		return nil, fmt.Errorf("auto-save end: %w", err)
	}
	return recurrence.Build(rule, r.StartDate.Format(dateLayout), &end, today)
}

// nextAutoDate — the first occurrence not produced yet; nil once it has ended.
func nextAutoDate(sp *recurrence.Spec, last *time.Time, today time.Time) *time.Time {
	until := today.AddDate(1, 0, 0)
	if sp.GeneratedUntil != nil && sp.GeneratedUntil.After(until) {
		until = *sp.GeneratedUntil
	}
	for _, d := range sp.Within(until) {
		if last == nil || d.After(*last) {
			return &d
		}
	}
	return nil
}

func legacyDay(sp *recurrence.Spec) int {
	if sp.Freq == "monthly" && sp.MonthlyMode != nil && *sp.MonthlyMode == "day" && sp.MonthDay != nil {
		return *sp.MonthDay
	}
	return sp.StartDate.Day()
}

// autoViews groups every auto-save by goal; monthPlan is for the month that
// starts on monthStart (the next_date stays relative to today).
func (s *Service) autoViews(ctx context.Context, userID uuid.UUID, today, monthStart time.Time) (map[uuid.UUID]*autoSet, error) {
	rows, err := s.store.db.Query(ctx, `SELECT id, COALESCE(goal_id, '00000000-0000-0000-0000-000000000000'::uuid),
		amount::float8, account_id, rule, start_date, end_rule, last_produced
		FROM saving_auto WHERE user_id = $1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("auto-saves: %w", err)
	}
	var recs []autoRow
	var accIDs []uuid.UUID
	for rows.Next() {
		var r autoRow
		if err := rows.Scan(&r.ID, &r.GoalKey, &r.Amount, &r.AccountID, &r.Rule, &r.StartDate, &r.End, &r.LastProduced); err != nil {
			rows.Close()
			return nil, err
		}
		if r.AccountID != nil {
			accIDs = append(accIDs, *r.AccountID)
		}
		recs = append(recs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	accs, err := shared.LoadAccountRefs(ctx, s.store.db, accIDs)
	if err != nil {
		return nil, err
	}
	monthEnd := monthStart.AddDate(0, 1, -1)
	out := map[uuid.UUID]*autoSet{}
	for i := range recs {
		r := &recs[i]
		sp, err := r.spec(today)
		if err != nil {
			return nil, err
		}
		it := AutoSaveItem{
			ID:        r.ID,
			Amount:    round2(r.Amount),
			Rule:      sp.APIRule(),
			StartDate: r.StartDate.Format(dateLayout),
			End:       sp.APIEnd(),
			NextDate:  dstr(nextAutoDate(sp, r.LastProduced, today)),
		}
		if r.AccountID != nil {
			if a, ok := accs[*r.AccountID]; ok {
				it.Wallet = &a
			}
		}
		set := out[r.GoalKey]
		if set == nil {
			set = &autoSet{items: []AutoSaveItem{}}
			out[r.GoalKey] = set
			set.legacy = &AutoSave{Amount: it.Amount, Day: legacyDay(sp), Wallet: it.Wallet, NextDate: it.NextDate}
		}
		set.items = append(set.items, it)
		inMonth := 0
		for _, d := range sp.Within(monthEnd) {
			if !d.Before(monthStart) {
				inMonth++
			}
		}
		set.monthPlan = round2(set.monthPlan + it.Amount*float64(inMonth))
	}
	return out, nil
}

func (a *autoSet) itemsOrEmpty() []AutoSaveItem {
	if a == nil {
		return []AutoSaveItem{}
	}
	return a.items
}

func (a *autoSet) legacyOrNil() *AutoSave {
	if a == nil {
		return nil
	}
	return a.legacy
}

// SetAutoSaves replaces the set: a listed id is updated in place (its
// produced-up-to marker stays), one without an id is added, the rest go.
func (s *Service) SetAutoSaves(ctx context.Context, userID uuid.UUID, goalID *uuid.UUID, items []AutoSaveInput) error {
	if len(items) > maxAutoSaves {
		return ErrValidation
	}
	today, err := s.today(ctx, userID)
	if err != nil {
		return err
	}
	type built struct {
		in   AutoSaveInput
		rule []byte
		end  []byte
		sp   *recurrence.Spec
	}
	list := make([]built, 0, len(items))
	seen := map[uuid.UUID]bool{}
	for _, in := range items {
		if in.Amount <= 0 || in.Rule == nil || in.StartDate == "" {
			return ErrValidation
		}
		if in.ID != nil {
			if seen[*in.ID] {
				return ErrValidation
			}
			seen[*in.ID] = true
		}
		sp, err := recurrence.Build(*in.Rule, in.StartDate, in.End, today)
		if err != nil {
			return err
		}
		rule, _ := json.Marshal(sp.APIRule())
		end, _ := json.Marshal(sp.APIEnd())
		list = append(list, built{in: in, rule: rule, end: end, sp: sp})
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
	for _, b := range list {
		if b.in.AccountID != nil {
			if err := checkWallet(ctx, tx, userID, *b.in.AccountID); err != nil {
				return err
			}
		}
	}
	rows, err := tx.Query(ctx, `SELECT id FROM saving_auto
		WHERE user_id = $1 AND goal_id IS NOT DISTINCT FROM $2 FOR UPDATE`, userID, goalID)
	if err != nil {
		return fmt.Errorf("auto-saves: %w", err)
	}
	existing := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		existing[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for id := range seen {
		if !existing[id] {
			return ErrValidation
		}
	}
	for id := range existing {
		if seen[id] {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM saving_auto WHERE id = $1`, id); err != nil {
			return fmt.Errorf("auto-saves: %w", err)
		}
	}
	for _, b := range list {
		amount := round2(b.in.Amount)
		if b.in.ID != nil {
			if _, err := tx.Exec(ctx, `UPDATE saving_auto SET amount = $2, account_id = $3, rule = $4, start_date = $5, end_rule = $6
				WHERE id = $1`, *b.in.ID, amount, b.in.AccountID, b.rule, b.sp.StartDate, b.end); err != nil {
				return fmt.Errorf("auto-saves: %w", err)
			}
			continue
		}
		id, _ := uuid.NewV7()
		if _, err := tx.Exec(ctx, `INSERT INTO saving_auto (id, user_id, goal_id, amount, account_id, rule, start_date, end_rule)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, userID, goalID, amount, b.in.AccountID, b.rule, b.sp.StartDate, b.end); err != nil {
			return fmt.Errorf("auto-saves: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// SetAutoSave is the old one-setting call: the set becomes one monthly item
// (the first existing one is kept, so a draft already made isn't made twice).
func (s *Service) SetAutoSave(ctx context.Context, userID uuid.UUID, goalID *uuid.UUID, r AutoSaveRequest) error {
	if r.Amount <= 0 || r.Day < 1 || r.Day > 31 {
		return ErrValidation
	}
	today, err := s.today(ctx, userID)
	if err != nil {
		return err
	}
	var first *uuid.UUID
	var id uuid.UUID
	err = s.store.db.QueryRow(ctx, `SELECT id FROM saving_auto
		WHERE user_id = $1 AND goal_id IS NOT DISTINCT FROM $2 ORDER BY created_at, id LIMIT 1`, userID, goalID).Scan(&id)
	if err == nil {
		first = &id
	}
	day := r.Day
	return s.SetAutoSaves(ctx, userID, goalID, []AutoSaveInput{{
		ID:        first,
		Amount:    r.Amount,
		AccountID: r.AccountID,
		Rule: &recurrence.RecurrenceRule{
			Freq: "monthly", Interval: 1,
			Monthly: &recurrence.Monthly{Mode: "day", Day: &day},
		},
		StartDate: today.Format(dateLayout),
		End:       &recurrence.RecurrenceEnd{Kind: "none"},
	}})
}

func (s *Service) ClearAutoSave(ctx context.Context, userID uuid.UUID, goalID *uuid.UUID) error {
	_, err := s.store.db.Exec(ctx, `DELETE FROM saving_auto WHERE user_id = $1 AND goal_id IS NOT DISTINCT FROM $2`, userID, goalID)
	return err
}

func (s *Service) PutAutoSaveGoal(ctx context.Context, userID, goalID uuid.UUID, r AutoSaveRequest) (*GoalDetail, error) {
	if err := s.SetAutoSave(ctx, userID, &goalID, r); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, goalID, entriesPerPage, false)
}

func (s *Service) PutAutoSavesGoal(ctx context.Context, userID, goalID uuid.UUID, items []AutoSaveInput) (*GoalDetail, error) {
	if err := s.SetAutoSaves(ctx, userID, &goalID, items); err != nil {
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

func (s *Service) PutAutoSavesSpare(ctx context.Context, userID uuid.UUID, items []AutoSaveInput) (*SpareDetail, error) {
	if err := s.SetAutoSaves(ctx, userID, nil, items); err != nil {
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
