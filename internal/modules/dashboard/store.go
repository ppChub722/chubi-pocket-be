package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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

// attentionTypes — notification types that can still await an answer
// (shared.AwaitsAnswer); a READ row of any other type has left the block.
var attentionTypes = []string{
	"contact_link_request", "account_invite", "split_created", "split_changed",
	"split_paid", "project_tx_recorded_for_you", "project_tx_changed",
}

// AttentionIDs — the ids of the caller's notifications that belong in the
// ต้องจัดการ block, newest first (inbox order): not dismissed AND (unread OR
// awaiting an answer). The awaiting rule is shared.AwaitsAnswer; SQL only
// narrows the candidates to unread rows and read rows of a type that can
// await.
func (s *Store) AttentionIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, type, payload, read_at, actioned_at, dismissed_at
		FROM notifications
		WHERE recipient_user_id = $1 AND dismissed_at IS NULL
		  AND (read_at IS NULL OR (actioned_at IS NULL AND type = ANY($2)))
		ORDER BY created_at DESC, id DESC`, userID, attentionTypes)
	if err != nil {
		return nil, fmt.Errorf("attention: %w", err)
	}
	defer rows.Close()
	out := make([]uuid.UUID, 0)
	for rows.Next() {
		var (
			id                              uuid.UUID
			typ                             string
			payload                         []byte
			readAt, actionedAt, dismissedAt *time.Time
		)
		if err := rows.Scan(&id, &typ, &payload, &readAt, &actionedAt, &dismissedAt); err != nil {
			return nil, err
		}
		if readAt == nil || shared.AwaitsAnswer(typ, actionedAt, dismissedAt, payload) {
			out = append(out, id)
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
