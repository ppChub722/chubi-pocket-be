package saving_goals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrGoalNotFound       = errors.New("saving goal not found")
	ErrEntryNotFound      = errors.New("saving entry not found")
	ErrValidation         = errors.New("invalid request")
	ErrSplitMismatch      = errors.New("the splits must add up to the amount, one per wallet that holds the money")
	ErrAccountNotFound    = errors.New("wallet not found")
	ErrWalletNotTracked   = errors.New("this wallet doesn't track its balance")
	ErrGoalNotActive      = errors.New("this goal isn't active")
	ErrGoalHasSavings     = errors.New("this goal still holds money; close it first")
	ErrInvalidTransition  = errors.New("this action doesn't apply in the goal's current state")
	ErrInsufficientSaving = errors.New("not enough saved there")
)

type InsufficientError struct{ Available float64 }

func (e *InsufficientError) Error() string { return ErrInsufficientSaving.Error() }
func (e *InsufficientError) Unwrap() error { return ErrInsufficientSaving }

type querier interface {
	shared.Querier
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func (s *Store) Pool() *pgxpool.Pool { return s.db }

const goalColumns = `id, user_id, name, description, note, icon_code, target_amount::float8, deadline, milestones,
	status, closed_at, created_at, updated_at`

func scanGoal(row pgx.Row) (*Goal, error) {
	var (
		g    Goal
		icon []byte
		ms   []int32
	)
	if err := row.Scan(&g.ID, &g.UserID, &g.Name, &g.Description, &g.Note, &icon, &g.TargetAmount, &g.Deadline, &ms,
		&g.Status, &g.ClosedAt, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err
	}
	g.IconCode = shared.IconFromJSON(icon)
	for _, m := range ms {
		g.Milestones = append(g.Milestones, int(m))
	}
	return &g, nil
}

func (s *Store) getGoal(ctx context.Context, q querier, userID, id uuid.UUID, lock bool) (*Goal, error) {
	sql := `SELECT ` + goalColumns + ` FROM saving_goals WHERE id = $1 AND user_id = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	g, err := scanGoal(q.QueryRow(ctx, sql, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGoalNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("goal: %w", err)
	}
	return g, nil
}

func (s *Store) listGoals(ctx context.Context, q querier, userID uuid.UUID, where string, args ...any) ([]*Goal, error) {
	rows, err := q.Query(ctx, `SELECT `+goalColumns+` FROM saving_goals WHERE user_id = $1 `+where,
		append([]any{userID}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("goals: %w", err)
	}
	defer rows.Close()
	var out []*Goal
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) saveGoal(ctx context.Context, q querier, g *Goal) error {
	var icon []byte
	if g.IconCode != nil {
		var err error
		if icon, err = json.Marshal(g.IconCode); err != nil {
			return err
		}
	}
	ms := make([]int32, len(g.Milestones))
	for i, m := range g.Milestones {
		ms[i] = int32(m)
	}
	args := []any{g.ID, g.UserID, g.Name, shared.CleanText(g.Description), shared.CleanText(g.Note), icon,
		g.TargetAmount, g.Deadline, ms, g.Status, g.ClosedAt}
	var err error
	if g.ID == uuid.Nil {
		g.ID, _ = uuid.NewV7()
		args[0] = g.ID
		_, err = q.Exec(ctx, `INSERT INTO saving_goals (id, user_id, name, description, note, icon_code, target_amount,
			deadline, milestones, status, closed_at, created_by_user_id)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11, $2)`, args...)
	} else {
		_, err = q.Exec(ctx, `UPDATE saving_goals SET name = $3, description = $4, note = $5, icon_code = $6::jsonb,
			target_amount = $7, deadline = $8, milestones = $9, status = $10, closed_at = $11, updated_by_user_id = $2
			WHERE id = $1 AND user_id = $2`, args...)
	}
	if err != nil {
		return fmt.Errorf("save goal: %w", err)
	}
	return nil
}

const entryColumns = `id, user_id, group_id, kind, account_id, from_bucket, from_goal_id, to_bucket, to_goal_id,
	amount::float8, date, note, source, created_at`

func scanEntry(row pgx.Row) (*entryRow, error) {
	var (
		e   entryRow
		acc *uuid.UUID
	)
	if err := row.Scan(&e.ID, &e.UserID, &e.GroupID, &e.Kind, &acc, &e.FromBucket, &e.FromGoalID, &e.ToBucket,
		&e.ToGoalID, &e.Amount, &e.Date, &e.Note, &e.Source, &e.CreatedAt); err != nil {
		return nil, err
	}
	if acc != nil {
		e.AccountID = *acc
	}
	return &e, nil
}

func (s *Store) getEntry(ctx context.Context, q querier, userID, id uuid.UUID, lock bool) (*entryRow, error) {
	sql := `SELECT ` + entryColumns + ` FROM saving_entries WHERE id = $1 AND user_id = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	e, err := scanEntry(q.QueryRow(ctx, sql, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("entry: %w", err)
	}
	return e, nil
}

func (s *Store) listEntries(ctx context.Context, q querier, userID uuid.UUID, where string, args ...any) ([]*entryRow, error) {
	rows, err := q.Query(ctx, `SELECT `+entryColumns+` FROM saving_entries WHERE user_id = $1 `+where,
		append([]any{userID}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("entries: %w", err)
	}
	defer rows.Close()
	var out []*entryRow
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) insertEntry(ctx context.Context, q querier, e *entryRow) error {
	if e.ID == uuid.Nil {
		e.ID, _ = uuid.NewV7()
	}
	e.CreatedAt = time.Now()
	var acc *uuid.UUID // no wallet = NULL (unallocated)
	if e.AccountID != uuid.Nil {
		acc = &e.AccountID
	}
	_, err := q.Exec(ctx, `INSERT INTO saving_entries (id, user_id, group_id, kind, account_id, from_bucket, from_goal_id,
		to_bucket, to_goal_id, amount, date, note, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		e.ID, e.UserID, e.GroupID, e.Kind, acc, e.FromBucket, e.FromGoalID, e.ToBucket, e.ToGoalID,
		e.Amount, e.Date, shared.CleanText(e.Note), e.Source)
	if err != nil {
		return fmt.Errorf("insert entry: %w", err)
	}
	return nil
}

func lockUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('saving:' || $1::text))`, userID)
	return err
}

func bucketBalances(ctx context.Context, q shared.Querier, userID uuid.UUID, goalID *uuid.UUID) (map[uuid.UUID]float64, error) {
	var (
		bals []shared.SavingBalance
		err  error
	)
	if goalID == nil {
		bals, err = shared.LoadSavingBalances(ctx, q, userID, `m.bucket = 'spare'`)
	} else {
		bals, err = shared.LoadSavingBalances(ctx, q, userID, `m.bucket = 'goal' AND m.goal_id = $2`, *goalID)
	}
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]float64{}
	for _, b := range bals {
		out[b.AccountID] += b.Amount
	}
	return out, nil
}

func checkNoNegative(ctx context.Context, q shared.Querier, userID, accountID uuid.UUID) error {
	var (
		bals []shared.SavingBalance
		err  error
	)
	if accountID == uuid.Nil {
		bals, err = shared.LoadSavingBalances(ctx, q, userID, `m.account_id IS NULL`)
	} else {
		bals, err = shared.LoadSavingBalances(ctx, q, userID, `m.account_id = $2`, accountID)
	}
	if err != nil {
		return err
	}
	for _, b := range bals {
		if b.Amount < -0.004 {
			return &InsufficientError{Available: 0}
		}
	}
	return nil
}

func checkWallet(ctx context.Context, q querier, userID, accountID uuid.UUID) error {
	var active, tracked bool
	err := q.QueryRow(ctx, `SELECT a.status = 'active' AND `+shared.ActiveMembershipPredicate("a.id", "$2")+`, a.track_balance
		FROM accounts a WHERE a.id = $1`, accountID, userID).Scan(&active, &tracked)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return ErrAccountNotFound
	}
	if err != nil {
		return err
	}
	if !tracked {
		return ErrWalletNotTracked
	}
	return nil
}

func (s *Store) userCurrency(ctx context.Context, userID uuid.UUID) (string, error) {
	var c string
	err := s.db.QueryRow(ctx, `SELECT currency FROM users WHERE id = $1`, userID).Scan(&c)
	return c, err
}

func (s *Store) userTimezone(ctx context.Context, userID uuid.UUID) (string, error) {
	var tz *string
	err := s.db.QueryRow(ctx, `SELECT preferences->>'timezone' FROM user_preferences WHERE user_id = $1`, userID).Scan(&tz)
	if errors.Is(err, pgx.ErrNoRows) || tz == nil {
		return "", nil
	}
	return *tz, err
}
