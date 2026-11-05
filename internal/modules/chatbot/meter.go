package chatbot

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TierOwner may use the chatbot; every other tier (free) may not yet.
const TierOwner = "owner"

// Call is one model call, for the usage log. Never the user's text.
type Call struct {
	Channel, Provider, Model, Status string
	In, Out, CacheRead, CacheWrite   int
	Ms                               int64
}

// Meter reads who may use the chatbot and how much they used this month, and logs each call.
// Nil on the Bot = no tier, no cap, no log (the fake provider, tests).
type Meter interface {
	Tier(ctx context.Context, userID uuid.UUID) (string, error)
	// Used counts the user's model calls since the start of the month.
	Used(ctx context.Context, userID uuid.UUID, since time.Time) (int, error)
	Record(ctx context.Context, userID uuid.UUID, c Call) error
}

// monthStart is midnight on the 1st in Asia/Bangkok, the cap's reset (owner 2026-10-30).
func monthStart(now time.Time) time.Time {
	n := now.In(bangkok)
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, bangkok)
}

// DBMeter is the Meter over the users and llm_calls tables.
type DBMeter struct{ db *pgxpool.Pool }

func NewDBMeter(db *pgxpool.Pool) *DBMeter { return &DBMeter{db: db} }

func (m *DBMeter) Tier(ctx context.Context, userID uuid.UUID) (string, error) {
	var tier string
	err := m.db.QueryRow(ctx, `SELECT tier FROM users WHERE id = $1`, userID).Scan(&tier)
	return tier, err
}

func (m *DBMeter) Used(ctx context.Context, userID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := m.db.QueryRow(ctx, `SELECT COUNT(*) FROM llm_calls WHERE user_id = $1 AND created_at >= $2`,
		userID, since).Scan(&n)
	return n, err
}

func (m *DBMeter) Record(ctx context.Context, userID uuid.UUID, c Call) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	_, err = m.db.Exec(ctx, `
		INSERT INTO llm_calls (id, user_id, channel, provider, model, tokens_in, tokens_out, cache_read, cache_write, ms, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		id, userID, c.Channel, c.Provider, c.Model, c.In, c.Out, c.CacheRead, c.CacheWrite, c.Ms, c.Status)
	return err
}
