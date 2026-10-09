package imports

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Import log kinds (migration 50) — what slip import met and can't handle
// yet. Written at once; read by hand (ops-runbook).
const (
	LogUnknownProvider = "unknown_provider" // QR bank code not in payment_providers
	LogUnsupportedBank = "unsupported_bank" // known bank, no slip rule set yet
	LogIncomplete      = "incomplete"       // amount / date didn't read
)

// ImportLog — one row of import_logs. Never the slip's text or image.
type ImportLog struct {
	UserID   uuid.UUID
	Kind     string
	Scheme   string
	Code     string
	TransRef string
	Details  map[string]any
}

// LogStore writes import_logs.
type LogStore struct{ db *pgxpool.Pool }

func NewLogStore(db *pgxpool.Pool) *LogStore { return &LogStore{db: db} }

func (s *LogStore) Write(ctx context.Context, l ImportLog) error {
	details := l.Details
	if details == nil {
		details = map[string]any{}
	}
	b, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("import log details: %w", err)
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO import_logs (user_id, kind, scheme, code, trans_ref, details)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6::jsonb)`,
		l.UserID, l.Kind, l.Scheme, l.Code, l.TransRef, b)
	if err != nil {
		return fmt.Errorf("insert import log: %w", err)
	}
	return nil
}
