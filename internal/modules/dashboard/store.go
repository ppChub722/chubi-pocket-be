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

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

type upcomingRow struct {
	ID       uuid.UUID
	Name     string
	Type     string
	Amount   float64
	DueDate  string
	IconCode *shared.IconCode
	LogoURL  *string
	// RuleID / Slot name a recurring occurrence read from an Expecter.
	RuleID *uuid.UUID
	Slot   *string
}

func (s *Store) UpcomingItems(ctx context.Context, userID uuid.UUID, today, until string) ([]upcomingRow, error) {
	rows, err := s.db.Query(ctx, `
		SELECT i.id, COALESCE(r.name, i.draft->>'description', ''), COALESCE(i.draft->>'type', 'expense'),
		       COALESCE((i.draft->>'amount')::numeric, 0), to_char(i.date, 'YYYY-MM-DD'), r.icon_code, NULL::text
		FROM recurring_items i LEFT JOIN recurring_rules r ON r.id = i.rule_id
		WHERE i.user_id = $1 AND i.produced_at IS NULL AND i.pending_id IS NULL AND i.transaction_id IS NULL
		  AND NOT i.archived AND i.date > $2::date AND i.date <= $3::date
		  AND r.status = 'active'
		ORDER BY i.date ASC, 2 ASC`, userID, today, until)
	if err != nil {
		return nil, fmt.Errorf("upcoming items: %w", err)
	}
	defer rows.Close()
	out := make([]upcomingRow, 0)
	for rows.Next() {
		var r upcomingRow
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

var attentionTypes = []string{
	"contact_link_request", "account_invite", "project_invite", "recurring_ending", "split_created", "split_changed",
	"project_tx_recorded_for_you", "project_tx_changed",
}

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

func (s *Store) UserCurrency(ctx context.Context, userID uuid.UUID) (string, error) {
	var cur string
	if err := s.db.QueryRow(ctx, `SELECT currency FROM users WHERE id = $1`, userID).Scan(&cur); err != nil {
		return "", fmt.Errorf("user currency: %w", err)
	}
	return cur, nil
}
