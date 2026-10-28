package telegram

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Received is one kept text (migration 120): the real sentences the LLM eval set (1b) is built from.
type Received struct {
	UpdateID     int64
	ChatID       int64
	FromUsername string // "" = none
	Text         string
	ReceivedAt   time.Time // the message's Telegram date
}

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Save keeps a text once per update_id; false = Telegram sent this update before.
func (s *Store) Save(ctx context.Context, r Received) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		INSERT INTO telegram_messages (update_id, chat_id, from_username, text, received_at)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5)
		ON CONFLICT (update_id) DO NOTHING`,
		r.UpdateID, r.ChatID, r.FromUsername, r.Text, r.ReceivedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
