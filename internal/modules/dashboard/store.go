package dashboard

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Store holds the two reads no other module exposes in the shape the
// dashboard needs. Every money aggregate goes through the owning
// module's service instead (report scope + reportable rules live there).
type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

type scheduledRow struct {
	ID       uuid.UUID
	Name     string
	Type     string
	Amount   float64
	DueDate  string
	IconCode *shared.IconCode
	LogoURL  *string
}

// DueScheduled — the caller's active scheduled transactions due on or
// before `until`, overdue ones included (nothing auto-generates them
// yet, so a past next_billing_date means "not recorded"). `until` is a
// date in the caller's timezone, computed by the service — never the
// DB's CURRENT_DATE.
func (s *Store) DueScheduled(ctx context.Context, userID uuid.UUID, until string) ([]scheduledRow, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, type, amount, to_char(next_billing_date, 'YYYY-MM-DD'), icon_code, logo_url
		FROM scheduled_transactions
		WHERE user_id = $1 AND status = 'active' AND next_billing_date <= $2::date
		ORDER BY next_billing_date ASC, name ASC`, userID, until)
	if err != nil {
		return nil, fmt.Errorf("due scheduled: %w", err)
	}
	defer rows.Close()
	out := make([]scheduledRow, 0)
	for rows.Next() {
		var r scheduledRow
		var icon []byte
		if err := rows.Scan(&r.ID, &r.Name, &r.Type, &r.Amount, &r.DueDate, &icon, &r.LogoURL); err != nil {
			return nil, err
		}
		if r.IconCode, err = decodeIcon(icon); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CategoryIcons — icon_code by category id (caller's own categories).
func (s *Store) CategoryIcons(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]*shared.IconCode, error) {
	out := make(map[uuid.UUID]*shared.IconCode, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT id, icon_code FROM categories WHERE user_id = $1 AND id = ANY($2)`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("category icons: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var icon []byte
		if err := rows.Scan(&id, &icon); err != nil {
			return nil, err
		}
		if out[id], err = decodeIcon(icon); err != nil {
			return nil, err
		}
	}
	return out, rows.Err()
}

func decodeIcon(b []byte) (*shared.IconCode, error) {
	if b == nil {
		return nil, nil
	}
	ic := new(shared.IconCode)
	if err := json.Unmarshal(b, ic); err != nil {
		return nil, fmt.Errorf("unmarshal icon_code: %w", err)
	}
	return ic, nil
}
