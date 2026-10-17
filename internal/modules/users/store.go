package users

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
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
	ErrEmailExists   = errors.New("email already registered to another account")
)

// GetProfile joins users + user_preferences for the GET /v1/users/me payload.
func (s *Store) GetProfile(ctx context.Context, id uuid.UUID) (*Profile, error) {
	q := `
		SELECT u.id, u.username, u.email, u.email_verified_at, u.display_name,
		       u.currency, u.icon_code, u.status, u.created_at, u.updated_at,
		       COALESCE(p.preferences, '{}'::jsonb)
		FROM users u
		LEFT JOIN user_preferences p ON p.user_id = u.id
		WHERE u.id = $1`

	var (
		prof      Profile
		iconBytes []byte
		prefRaw   []byte
		prefs     map[string]any
	)
	err := s.db.QueryRow(ctx, q, id).Scan(
		&prof.ID, &prof.Username, &prof.Email, &prof.EmailVerifiedAt, &prof.DisplayName,
		&prof.Currency, &iconBytes, &prof.Status, &prof.CreatedAt, &prof.UpdatedAt,
		&prefRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	if iconBytes != nil {
		prof.IconCode = new(shared.IconCode)
		if err := json.Unmarshal(iconBytes, prof.IconCode); err != nil {
			return nil, fmt.Errorf("decode icon_code: %w", err)
		}
	}
	if err := json.Unmarshal(prefRaw, &prefs); err != nil {
		return nil, fmt.Errorf("decode preferences: %w", err)
	}
	prof.Preferences = prefsFromMap(prefs)
	prof.Status = apiStatus(prof.Status)
	if err := s.loadSettings(ctx, &prof); err != nil {
		return nil, err
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

	if req.DisplayName != nil || req.IconCode != nil || req.Email != nil {
		q := `UPDATE users SET updated_by_user_id = $1`
		args := []any{id}

		if req.DisplayName != nil {
			args = append(args, *req.DisplayName)
			q += fmt.Sprintf(", display_name = $%d", len(args))
		}
		if req.IconCode != nil {
			b, err := json.Marshal(req.IconCode)
			if err != nil {
				return fmt.Errorf("marshal icon_code: %w", err)
			}
			args = append(args, b)
			q += fmt.Sprintf(", icon_code = $%d::jsonb", len(args))
		}
		if req.Email != nil {
			args = append(args, *req.Email)
			q += fmt.Sprintf(", email = $%d", len(args))
		}
		q += " WHERE id = $1"

		if _, err := tx.Exec(ctx, q, args...); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
				strings.Contains(pgErr.ConstraintName, "email") {
				return ErrEmailExists
			}
			return fmt.Errorf("update users: %w", err)
		}
	}

	if req.Currency != nil {
		cur := strings.ToUpper(*req.Currency)
		// Relabel only: the column follows the setting, amounts are untouched.
		for _, q := range []string{
			`UPDATE users SET currency = $2, updated_by_user_id = $1 WHERE id = $1`,
			`UPDATE accounts SET currency = $2 WHERE user_id = $1`,
			`UPDATE personal_debts SET currency = $2 WHERE user_id = $1`,
			`UPDATE project_transactions SET currency = $2 WHERE record_user_id = $1`,
		} {
			if _, err := tx.Exec(ctx, q, id, cur); err != nil {
				return fmt.Errorf("relabel currency: %w", err)
			}
		}
	}

	acc, accSet, err := req.defaultAccountChange()
	if err != nil {
		return err
	}
	if err := saveSettingsTx(ctx, tx, id, acc, accSet, req.AutoCopyOwnEventBills); err != nil {
		return err
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
