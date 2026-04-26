package users

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

var (
	ErrUserNotFound  = errors.New("user not found")
	ErrAlreadyActive = errors.New("account is already active")
)

// GetProfile joins users + user_preferences for the GET /v1/users/me payload.
func (s *Store) GetProfile(ctx context.Context, id uuid.UUID) (*Profile, error) {
	q := `
		SELECT u.id, u.username, u.email, u.email_verified_at, u.display_name,
		       u.currency, u.avatar_url, u.status, u.created_at, u.updated_at,
		       COALESCE(p.preferences, '{}'::jsonb)
		FROM users u
		LEFT JOIN user_preferences p ON p.user_id = u.id
		WHERE u.id = $1`

	var (
		prof    Profile
		prefRaw []byte
	)
	err := s.db.QueryRow(ctx, q, id).Scan(
		&prof.ID, &prof.Username, &prof.Email, &prof.EmailVerifiedAt, &prof.DisplayName,
		&prof.Currency, &prof.AvatarURL, &prof.Status, &prof.CreatedAt, &prof.UpdatedAt,
		&prefRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	if err := json.Unmarshal(prefRaw, &prof.Preferences); err != nil {
		return nil, fmt.Errorf("decode preferences: %w", err)
	}
	return &prof, nil
}

// UpdateProfile applies a partial update to users + user_preferences in one tx.
// Preferences are JSONB-merged (`||`), so only provided keys change.
func (s *Store) UpdateProfile(ctx context.Context, id uuid.UUID, req UpdateProfileRequest) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if req.DisplayName != nil || req.AvatarURL != nil || req.Currency != nil {
		_, err := tx.Exec(ctx, `
			UPDATE users SET
				display_name = COALESCE($2, display_name),
				avatar_url   = COALESCE($3, avatar_url),
				currency     = COALESCE($4, currency),
				updated_by_user_id = $1
			WHERE id = $1`,
			id, req.DisplayName, req.AvatarURL, req.Currency)
		if err != nil {
			return fmt.Errorf("update users: %w", err)
		}
	}

	if len(req.Preferences) > 0 {
		patch, err := json.Marshal(req.Preferences)
		if err != nil {
			return fmt.Errorf("encode preferences patch: %w", err)
		}
		_, err = tx.Exec(ctx, `
			UPDATE user_preferences
			SET preferences = preferences || $2::jsonb,
			    updated_by_user_id = $1
			WHERE user_id = $1`,
			id, patch)
		if err != nil {
			return fmt.Errorf("merge preferences: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (s *Store) SetStatus(ctx context.Context, id uuid.UUID, status string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE users SET status = $2, updated_by_user_id = $1 WHERE id = $1`,
		id, status)
	if err != nil {
		return fmt.Errorf("db error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}
