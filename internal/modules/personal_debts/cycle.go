package personal_debts

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type cycleMark struct {
	Date    string
	Created time.Time
	ID      uuid.UUID
}

func (s *Store) cycleBoundary(ctx context.Context, q shared.Querier, userID uuid.UUID, key string) (*cycleMark, error) {
	rows, err := q.Query(ctx, `
		SELECT to_char(date, 'YYYY-MM-DD'), created_at, id FROM (
			SELECT pd.date, pd.created_at, pd.id,
			       SUM(`+shared.DebtSignedSQL("pd")+`) OVER (ORDER BY pd.date, pd.created_at, pd.id) AS run
			FROM personal_debts pd
			WHERE pd.user_id = $1 AND `+shared.DebtKeySQL("pd")+` = $2) x
		WHERE ABS(run) < 0.005
		ORDER BY date DESC, created_at DESC, id DESC LIMIT 1`, userID, key)
	if err != nil {
		return nil, fmt.Errorf("cycle boundary: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var m cycleMark
	if err := rows.Scan(&m.Date, &m.Created, &m.ID); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *cycleMark) after(date string, created time.Time, id uuid.UUID) bool {
	if m == nil {
		return true
	}
	switch {
	case date != m.Date:
		return date > m.Date
	case !created.Equal(m.Created):
		return created.After(m.Created)
	}
	return id.String() > m.ID.String()
}

