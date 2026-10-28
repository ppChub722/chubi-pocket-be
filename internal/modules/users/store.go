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
	if err := saveSettingsTx(ctx, tx, id, acc, accSet, req.settingSwitches()); err != nil {
		return err
	}

	prefs := req.Preferences
	if pages, ok := prefs[PrefPagePrefs]; ok {
		if err := savePagePrefsTx(ctx, tx, id, pages); err != nil {
			return err
		}
		prefs = make(map[string]any, len(req.Preferences))
		for k, v := range req.Preferences {
			if k != PrefPagePrefs {
				prefs[k] = v
			}
		}
	}
	if len(prefs) > 0 {
		patch, err := json.Marshal(prefs)
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

// savePagePrefsTx — page_prefs merged per page (P6): a page's object replaces its entry, a null
// entry removes it, a null page_prefs removes every page.
func savePagePrefsTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, pages any) error {
	if pages == nil {
		_, err := tx.Exec(ctx, `UPDATE user_preferences SET preferences = preferences - 'page_prefs',
			updated_by_user_id = $1 WHERE user_id = $1`, userID)
		return err
	}
	set := map[string]any{}
	reset := []string{}
	for page, entry := range pages.(map[string]any) {
		if entry == nil {
			reset = append(reset, page)
		} else {
			set[page] = entry
		}
	}
	patch, err := json.Marshal(set)
	if err != nil {
		return fmt.Errorf("encode page_prefs: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE user_preferences
		SET preferences = jsonb_set(preferences, '{page_prefs}',
		        (CASE WHEN jsonb_typeof(preferences->'page_prefs') = 'object' THEN preferences->'page_prefs'
		              ELSE '{}'::jsonb END || $2::jsonb) - $3::text[]),
		    updated_by_user_id = $1
		WHERE user_id = $1`, userID, patch, reset)
	if err != nil {
		return fmt.Errorf("merge page_prefs: %w", err)
	}
	return nil
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
