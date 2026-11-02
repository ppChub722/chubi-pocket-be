package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// touchUsers bumps users.updated_at: a link / unlink / new picture can change their avatar, and
// the sync fingerprints of everyone holding them as a linked contact read users.updated_at.
func touchUsers(ctx context.Context, q execer, ids ...uuid.UUID) error {
	if _, err := q.Exec(ctx, `UPDATE users SET updated_at = NOW() WHERE id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("touch users: %w", err)
	}
	return nil
}

// link is one user_connections row.
type link struct {
	ExternalID  string
	Account     *string
	LinkedAt    time.Time
	Permissions map[string]bool
}

type photoRow struct {
	ID         uuid.UUID
	Source     string
	Version    int
	ImportedAt time.Time
}

// links — every app the user linked, by provider.
func (s *Store) links(ctx context.Context, userID uuid.UUID) (map[string]link, error) {
	rows, err := s.db.Query(ctx, `SELECT provider, external_id, account_label, linked_at, permissions
		FROM user_connections WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("connections: %w", err)
	}
	defer rows.Close()
	out := map[string]link{}
	for rows.Next() {
		var (
			p     string
			l     link
			perms []byte
		)
		if err := rows.Scan(&p, &l.ExternalID, &l.Account, &l.LinkedAt, &perms); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(perms, &l.Permissions); err != nil {
			return nil, fmt.Errorf("permissions: %w", err)
		}
		out[p] = l
	}
	return out, rows.Err()
}

// photos — the user's stored pictures, by source.
func (s *Store) photos(ctx context.Context, userID uuid.UUID) (map[string]photoRow, error) {
	rows, err := s.db.Query(ctx, `SELECT id, source, version, imported_at FROM user_avatars WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("avatars: %w", err)
	}
	defer rows.Close()
	out := map[string]photoRow{}
	for rows.Next() {
		var p photoRow
		if err := rows.Scan(&p.ID, &p.Source, &p.Version, &p.ImportedAt); err != nil {
			return nil, err
		}
		out[p.Source] = p
	}
	return out, rows.Err()
}

// linkTx makes externalID the user's account in that app. The account leaves any other user it
// was linked to, and the user's old account in that app is replaced.
func linkTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, provider, externalID, account string,
	perms map[string]bool) error {
	// the chat may belong to another account; moving it must not lock that account out (1b-2)
	var owner uuid.UUID
	err := tx.QueryRow(ctx, `SELECT user_id FROM user_connections WHERE provider = $1 AND external_id = $2`,
		provider, externalID).Scan(&owner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("chat owner: %w", err)
	}
	if err == nil && owner != userID {
		if last, err := lastLoginMethod(ctx, tx, owner); err != nil {
			return err
		} else if last {
			return shared.ErrLastLoginMethod
		}
	}
	rows, err := tx.Query(ctx, `DELETE FROM user_connections WHERE provider = $1 AND (external_id = $2 OR user_id = $3)
		RETURNING user_id`, provider, externalID, userID)
	if err != nil {
		return fmt.Errorf("old links: %w", err)
	}
	touched := []uuid.UUID{userID}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		touched = append(touched, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := touchUsers(ctx, tx, touched...); err != nil {
		return err
	}
	pj, err := json.Marshal(perms)
	if err != nil {
		return err
	}
	var label *string
	if account != "" {
		label = &account
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_connections (user_id, provider, external_id, account_label, permissions)
		VALUES ($1, $2, $3, $4, $5::jsonb)`, userID, provider, externalID, label, pj); err != nil {
		return fmt.Errorf("link: %w", err)
	}
	return nil
}

// unlink — shared.ErrLastLoginMethod when it is the passwordless account's only linked app.
func (s *Store) unlink(ctx context.Context, userID uuid.UUID, provider string) error {
	var linked bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_connections WHERE user_id = $1 AND provider = $2)`,
		userID, provider).Scan(&linked); err != nil || !linked {
		return err
	}
	if last, err := lastLoginMethod(ctx, s.db, userID); err != nil {
		return err
	} else if last {
		return shared.ErrLastLoginMethod
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM user_connections WHERE user_id = $1 AND provider = $2`, userID, provider); err != nil {
		return err
	}
	return touchUsers(ctx, s.db, userID)
}

// unlinkExternal — the account in that app unlinks itself (/unlink in the chat); false = it wasn't
// linked; shared.ErrLastLoginMethod as unlink.
func (s *Store) unlinkExternal(ctx context.Context, provider, externalID string) (bool, error) {
	userID, _, ok, err := s.byExternal(ctx, provider, externalID)
	if err != nil || !ok {
		return false, err
	}
	if last, err := lastLoginMethod(ctx, s.db, userID); err != nil {
		return false, err
	} else if last {
		return false, shared.ErrLastLoginMethod
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM user_connections WHERE provider = $1 AND external_id = $2`,
		provider, externalID); err != nil {
		return false, err
	}
	return true, touchUsers(ctx, s.db, userID)
}

// byExternal — who an account in that app is linked to; ok false = nobody.
func (s *Store) byExternal(ctx context.Context, provider, externalID string) (userID uuid.UUID, perms map[string]bool, ok bool, err error) {
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT user_id, permissions FROM user_connections WHERE provider = $1 AND external_id = $2`,
		provider, externalID).Scan(&userID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil, false, nil
	}
	if err != nil {
		return uuid.Nil, nil, false, err
	}
	if err := json.Unmarshal(raw, &perms); err != nil {
		return uuid.Nil, nil, false, err
	}
	return userID, perms, true, nil
}

// setPermissions merges {key: bool}; false = not linked.
func (s *Store) setPermissions(ctx context.Context, userID uuid.UUID, provider string, patch map[string]bool) (bool, error) {
	b, err := json.Marshal(patch)
	if err != nil {
		return false, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE user_connections SET permissions = permissions || $3::jsonb
		WHERE user_id = $1 AND provider = $2`, userID, provider, b)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// savePhoto stores (or replaces) the user's picture from one source; the version goes up.
func (s *Store) savePhoto(ctx context.Context, userID uuid.UUID, source string, image []byte) error {
	if _, err := s.db.Exec(ctx, `INSERT INTO user_avatars (user_id, source, image) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, source) DO UPDATE
		SET image = EXCLUDED.image, version = user_avatars.version + 1, imported_at = NOW()`, userID, source, image); err != nil {
		return err
	}
	return touchUsers(ctx, s.db, userID)
}

// image — a stored picture by id; ok false = gone.
func (s *Store) image(ctx context.Context, id uuid.UUID) ([]byte, bool, error) {
	var b []byte
	err := s.db.QueryRow(ctx, `SELECT image FROM user_avatars WHERE id = $1`, id).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}
