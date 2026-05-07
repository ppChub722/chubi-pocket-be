package saving_goals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

func (s *Store) Pool() *pgxpool.Pool { return s.db }

var (
	ErrGoalNotFound       = errors.New("saving goal not found")
	ErrAccountNotOwned    = errors.New("linked account does not belong to caller")
	ErrAllocationExceeded = errors.New("allocation sum on account would exceed 100")
)

// `deadline::text` cast: pgx's binary protocol decodes Postgres DATE
// into pgtype.Date, not *string — without the ::text cast the scanner
// errors with "cannot scan date (OID 1082) in binary format into **string".
const goalColumns = `id, user_id, name, target_amount, linked_account_id,
	allocation_pct, deadline::text, icon_code, status, note,
	created_at, updated_at`

func scanGoal(row pgx.Row) (*SavingGoal, error) {
	var g SavingGoal
	var iconBytes []byte
	var deadline *string
	if err := row.Scan(
		&g.ID, &g.UserID, &g.Name, &g.TargetAmount, &g.LinkedAccountID,
		&g.AllocationPct, &deadline, &iconBytes, &g.Status, &g.Note,
		&g.CreatedAt, &g.UpdatedAt,
	); err != nil {
		return nil, err
	}
	g.Deadline = deadline
	if iconBytes != nil {
		g.IconCode = new(shared.IconCode)
		if err := json.Unmarshal(iconBytes, g.IconCode); err != nil {
			return nil, fmt.Errorf("unmarshal icon_code: %w", err)
		}
	}
	return &g, nil
}

// AccountSnapshot is a tiny read used both during create-time
// validation and to build the LinkedAccount embed on read responses.
type AccountSnapshot struct {
	ID       uuid.UUID
	Name     string
	Balance  float64
	Currency string
}

// VerifyAccountOwnership returns the account snapshot iff `accountID`
// belongs to `userID` and is non-archived. Used at create / update
// time before mutating saving_goals rows.
func (s *Store) VerifyAccountOwnership(
	ctx context.Context, q pgxQuerier, userID, accountID uuid.UUID,
) (*AccountSnapshot, error) {
	var a AccountSnapshot
	err := q.QueryRow(ctx,
		`SELECT id, name, balance, currency
		   FROM accounts
		  WHERE id = $1 AND user_id = $2 AND status = 'active'`,
		accountID, userID).
		Scan(&a.ID, &a.Name, &a.Balance, &a.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccountNotOwned
	}
	if err != nil {
		return nil, fmt.Errorf("verify account: %w", err)
	}
	return &a, nil
}

// SumActiveAllocationOnAccountForUpdate locks the active rows on
// `accountID` (FOR UPDATE) and returns the sum of allocation_pct,
// optionally excluding `excludeGoalID` (for update / restore where
// we don't want to double-count the row we're changing).
//
// Must be called inside a tx; the SELECT FOR UPDATE serializes
// concurrent create / update / restore on the same account so the
// sum-≤-100 invariant can't be raced past.
//
// Postgres rejects FOR UPDATE on a query with an aggregate
// (SQLSTATE 0A000), so the lock has to live in a CTE that returns
// raw rows; the outer SELECT aggregates the locked snapshot.
func (s *Store) SumActiveAllocationOnAccountForUpdate(
	ctx context.Context, tx pgx.Tx,
	accountID uuid.UUID, excludeGoalID *uuid.UUID,
) (float64, error) {
	args := []any{accountID}
	exclusion := ""
	if excludeGoalID != nil {
		args = append(args, *excludeGoalID)
		exclusion = " AND id <> $2"
	}
	q := `WITH locked AS (
	          SELECT allocation_pct
	            FROM saving_goals
	           WHERE linked_account_id = $1
	             AND status = 'active'` + exclusion + `
	           FOR UPDATE
	      )
	      SELECT COALESCE(SUM(allocation_pct), 0) FROM locked`
	var sum float64
	if err := tx.QueryRow(ctx, q, args...).Scan(&sum); err != nil {
		return 0, fmt.Errorf("sum allocation: %w", err)
	}
	return sum, nil
}

// CreateInTx inserts the row inside the caller's tx. Caller must have
// already validated ownership + allocation sum.
func (s *Store) CreateInTx(
	ctx context.Context, tx pgx.Tx, userID uuid.UUID, req CreateRequest, allocation float64,
) (*SavingGoal, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}

	var iconBytes []byte
	if req.IconCode != nil {
		iconBytes, err = json.Marshal(req.IconCode)
		if err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
	}

	q := `INSERT INTO saving_goals
		(id, user_id, name, target_amount, linked_account_id,
		 allocation_pct, deadline, icon_code, note,
		 created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $2, $2)
		RETURNING ` + goalColumns
	return scanGoal(tx.QueryRow(ctx, q,
		id, userID, strings.TrimSpace(req.Name), req.TargetAmount,
		req.LinkedAccountID, allocation, req.Deadline, iconBytes, req.Note,
	))
}

func (s *Store) GetByID(ctx context.Context, userID, id uuid.UUID) (*SavingGoal, error) {
	q := `SELECT ` + goalColumns + ` FROM saving_goals WHERE id = $1 AND user_id = $2`
	g, err := scanGoal(s.db.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGoalNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return g, nil
}

// GetByIDInTx is the tx-bound variant for update/restore paths
// where we already hold the FOR UPDATE lock on sibling rows.
func (s *Store) GetByIDInTx(
	ctx context.Context, tx pgx.Tx, userID, id uuid.UUID,
) (*SavingGoal, error) {
	q := `SELECT ` + goalColumns + ` FROM saving_goals
	       WHERE id = $1 AND user_id = $2 FOR UPDATE`
	g, err := scanGoal(tx.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGoalNotFound
	}
	return g, err
}

func (s *Store) List(
	ctx context.Context, userID uuid.UUID, f ListFilter,
) ([]SavingGoal, error) {
	whereClauses := []string{"user_id = $1"}
	args := []any{userID}

	switch strings.ToLower(strings.TrimSpace(f.Status)) {
	case "", "active":
		whereClauses = append(whereClauses, "status = 'active'")
	case "archived":
		whereClauses = append(whereClauses, "status = 'archived'")
	case "all":
		// no filter
	}
	if f.AccountID != nil {
		args = append(args, *f.AccountID)
		whereClauses = append(whereClauses, fmt.Sprintf("linked_account_id = $%d", len(args)))
	}

	q := `SELECT ` + goalColumns + ` FROM saving_goals
	       WHERE ` + strings.Join(whereClauses, " AND ") + `
	       ORDER BY created_at DESC`

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	out := make([]SavingGoal, 0)
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// ListAccountSnapshotsForGoals batch-fetches account snapshots for the
// distinct linked_account_ids in `goals`. One query — saves N round-trips.
func (s *Store) ListAccountSnapshotsForGoals(
	ctx context.Context, userID uuid.UUID, goals []SavingGoal,
) (map[uuid.UUID]AccountSnapshot, error) {
	if len(goals) == 0 {
		return map[uuid.UUID]AccountSnapshot{}, nil
	}
	seen := make(map[uuid.UUID]struct{}, len(goals))
	ids := make([]uuid.UUID, 0, len(goals))
	for _, g := range goals {
		if _, ok := seen[g.LinkedAccountID]; ok {
			continue
		}
		seen[g.LinkedAccountID] = struct{}{}
		ids = append(ids, g.LinkedAccountID)
	}
	rows, err := s.db.Query(ctx,
		`SELECT id, name, balance, currency
		   FROM accounts
		  WHERE user_id = $1 AND id = ANY($2::uuid[])`,
		userID, ids)
	if err != nil {
		return nil, fmt.Errorf("snapshots: %w", err)
	}
	defer rows.Close()
	out := make(map[uuid.UUID]AccountSnapshot, len(ids))
	for rows.Next() {
		var a AccountSnapshot
		if err := rows.Scan(&a.ID, &a.Name, &a.Balance, &a.Currency); err != nil {
			return nil, err
		}
		out[a.ID] = a
	}
	return out, rows.Err()
}

// UpdateInTx applies the partial patch inside the caller's tx.
// Caller has already locked sibling rows + verified allocation sum.
func (s *Store) UpdateInTx(
	ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, req UpdateRequest,
) (*SavingGoal, error) {
	q := `UPDATE saving_goals SET updated_by_user_id = $1`
	args := []any{userID}

	if req.Name != nil {
		args = append(args, strings.TrimSpace(*req.Name))
		q += fmt.Sprintf(", name = $%d", len(args))
	}
	if req.TargetAmount != nil {
		args = append(args, *req.TargetAmount)
		q += fmt.Sprintf(", target_amount = $%d", len(args))
	}
	if req.AllocationPct != nil {
		args = append(args, *req.AllocationPct)
		q += fmt.Sprintf(", allocation_pct = $%d", len(args))
	}
	if req.Deadline != nil {
		// "" clears, otherwise YYYY-MM-DD.
		if *req.Deadline == "" {
			q += ", deadline = NULL"
		} else {
			args = append(args, *req.Deadline)
			q += fmt.Sprintf(", deadline = $%d", len(args))
		}
	}
	if req.IconCode != nil {
		b, err := json.Marshal(req.IconCode)
		if err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
		args = append(args, b)
		q += fmt.Sprintf(", icon_code = $%d::jsonb", len(args))
	}
	if req.Note != nil {
		args = append(args, *req.Note)
		q += fmt.Sprintf(", note = $%d", len(args))
	}

	args = append(args, id)
	q += fmt.Sprintf(" WHERE id = $%d AND user_id = $1 RETURNING ", len(args)) + goalColumns

	g, err := scanGoal(tx.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGoalNotFound
	}
	return g, err
}

// SetStatusInTx flips status (archive / restore). Caller validated.
func (s *Store) SetStatusInTx(
	ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, status string,
) (*SavingGoal, error) {
	q := `UPDATE saving_goals
	         SET status = $1, updated_by_user_id = $2
	       WHERE id = $3 AND user_id = $2
	   RETURNING ` + goalColumns
	g, err := scanGoal(tx.QueryRow(ctx, q, status, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGoalNotFound
	}
	return g, err
}

func (s *Store) Delete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM saving_goals WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrGoalNotFound
	}
	return nil
}

// pgxQuerier covers both *pgxpool.Pool and pgx.Tx so VerifyAccountOwnership
// can be called from either.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
