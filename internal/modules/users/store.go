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
	// ErrAvatarSourceUnavailable — avatar_settings.source names a picture I don't have (or an app
	// no longer linked).
	ErrAvatarSourceUnavailable = errors.New("no picture from that source")
)

func (s *Store) GetProfile(ctx context.Context, id uuid.UUID) (*Profile, error) {
	q := `
		SELECT u.id, u.username, u.email, u.email_verified_at, u.display_name,
		       u.currency, ` + shared.AvatarSQL("u.id", "NULL") + `, u.avatar_settings, u.password_hash IS NOT NULL, u.status, u.created_at, u.updated_at,
		       COALESCE(p.preferences, '{}'::jsonb)
		FROM users u
		LEFT JOIN user_preferences p ON p.user_id = u.id
		WHERE u.id = $1`

	var (
		prof        Profile
		avatarSrc   []byte
		settingsRaw []byte
		prefRaw     []byte
		prefs       map[string]any
	)
	err := s.db.QueryRow(ctx, q, id).Scan(
		&prof.ID, &prof.Username, &prof.Email, &prof.EmailVerifiedAt, &prof.DisplayName,
		&prof.Currency, &avatarSrc, &settingsRaw, &prof.HasPassword, &prof.Status, &prof.CreatedAt, &prof.UpdatedAt,
		&prefRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	prof.Avatar = shared.AvatarFromSrc(prof.DisplayName, avatarSrc)
	prof.AvatarSettings = shared.DefaultAvatarSettings()
	if err := json.Unmarshal(settingsRaw, &prof.AvatarSettings); err != nil {
		return nil, fmt.Errorf("decode avatar_settings: %w", err)
	}
	// only set colours go out (never "" / null entries)
	prof.AvatarSettings.Colors = shared.CleanColors(prof.AvatarSettings.Colors)
	if prof.AvatarSettings.Border != nil {
		prof.AvatarSettings.Border.Colors = shared.CleanColors(prof.AvatarSettings.Border.Colors)
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

	if req.AvatarSettings != nil {
		ok, err := avatarSourceUsableTx(ctx, tx, id, req.AvatarSettings.Source)
		if err != nil {
			return err
		}
		if !ok {
			return ErrAvatarSourceUnavailable
		}
		b, err := json.Marshal(req.AvatarSettings)
		if err != nil {
			return fmt.Errorf("marshal avatar_settings: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET avatar_settings = $2::jsonb, updated_by_user_id = $1 WHERE id = $1`,
			id, b); err != nil {
			return fmt.Errorf("update avatar_settings: %w", err)
		}
	}

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

// avatarSourceUsableTx — icon always; a picture source only when I have that picture and, for an
// app, the app is still linked (the same test as avatar_src()).
func avatarSourceUsableTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, source string) (bool, error) {
	if source == shared.AvatarSourceIcon {
		return true, nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM user_avatars a WHERE a.user_id = $1 AND a.source = $2
		   AND (a.source = 'upload' OR EXISTS (
		        SELECT 1 FROM user_connections c WHERE c.user_id = $1 AND c.provider = a.source)))`,
		userID, source).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("avatar source: %w", err)
	}
	return ok, nil
}

// DeleteAvatar removes my stored picture of one source; when it was the picked source the pick
// goes back to the icon. No picture = nothing to do.
func (s *Store) DeleteAvatar(ctx context.Context, userID uuid.UUID, source string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM user_avatars WHERE user_id = $1 AND source = $2`, userID, source); err != nil {
		return fmt.Errorf("delete avatar: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET avatar_settings = jsonb_set(avatar_settings, '{source}', '"icon"'),
			updated_by_user_id = $1
		WHERE id = $1 AND avatar_settings->>'source' = $2`, userID, source); err != nil {
		return fmt.Errorf("reset avatar source: %w", err)
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
