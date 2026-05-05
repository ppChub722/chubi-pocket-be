package tags

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
	ErrTagNotFound         = errors.New("tag not found")
	ErrTagExists           = errors.New("tag with that name already exists")
	ErrTransactionNotOwned = errors.New("transaction not found or not owned")
)

const tagColumns = `id, user_id, name, icon_code, created_at, updated_at`

func scanTag(row pgx.Row) (*Tag, error) {
	var t Tag
	var iconBytes []byte
	err := row.Scan(&t.ID, &t.UserID, &t.Name, &iconBytes, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if iconBytes != nil {
		t.IconCode = new(shared.IconCode)
		if err := json.Unmarshal(iconBytes, t.IconCode); err != nil {
			return nil, fmt.Errorf("unmarshal icon_code: %w", err)
		}
	}
	return &t, nil
}

func scanTagWithCount(row pgx.Row) (*Tag, error) {
	var t Tag
	var iconBytes []byte
	err := row.Scan(&t.ID, &t.UserID, &t.Name, &iconBytes, &t.CreatedAt, &t.UpdatedAt, &t.UsageCount)
	if err != nil {
		return nil, err
	}
	if iconBytes != nil {
		t.IconCode = new(shared.IconCode)
		if err := json.Unmarshal(iconBytes, t.IconCode); err != nil {
			return nil, fmt.Errorf("unmarshal icon_code: %w", err)
		}
	}
	return &t, nil
}

func (s *Store) Create(ctx context.Context, userID uuid.UUID, name string, iconCode *shared.IconCode) (*Tag, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	var iconJSON []byte
	if iconCode != nil {
		if iconJSON, err = json.Marshal(iconCode); err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
	}
	q := `INSERT INTO tags (id, user_id, name, icon_code, created_by_user_id)
		VALUES ($1, $2, $3, $4::jsonb, $2)
		RETURNING ` + tagColumns
	t, err := scanTag(s.db.QueryRow(ctx, q, id, userID, name, iconJSON))
	if err != nil {
		return nil, mapInsertError(err)
	}
	return t, nil
}

func (s *Store) GetByID(ctx context.Context, userID, id uuid.UUID) (*Tag, error) {
	q := `SELECT t.id, t.user_id, t.name, t.icon_code, t.created_at, t.updated_at,
		COALESCE((SELECT COUNT(*) FROM transaction_tags WHERE tag_id = t.id), 0)
		FROM tags t WHERE t.id = $1 AND t.user_id = $2`
	t, err := scanTagWithCount(s.db.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTagNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return t, nil
}

func (s *Store) List(ctx context.Context, userID uuid.UUID) ([]Tag, error) {
	// Spec §3.9 — server sorts by usage_count DESC, then name ASC, so the
	// user's most-used tags surface first in pickers.
	q := `SELECT t.id, t.user_id, t.name, t.icon_code, t.created_at, t.updated_at,
		COALESCE(c.cnt, 0) AS usage_count
		FROM tags t
		LEFT JOIN (
			SELECT tag_id, COUNT(*) AS cnt FROM transaction_tags GROUP BY tag_id
		) c ON c.tag_id = t.id
		WHERE t.user_id = $1
		ORDER BY COALESCE(c.cnt, 0) DESC, LOWER(t.name)`
	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	defer rows.Close()

	out := make([]Tag, 0)
	for rows.Next() {
		t, err := scanTagWithCount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// --- Attach / detach ---

// IsTransactionOwnedByUser checks ownership without importing transactions.
// Returns ErrTransactionNotOwned if the row doesn't exist or belongs to
// another user.
func (s *Store) IsTransactionOwnedByUser(ctx context.Context, txID, userID uuid.UUID) error {
	var owner uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT user_id FROM transactions WHERE id = $1`, txID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTransactionNotOwned
	}
	if err != nil {
		return fmt.Errorf("ownership check: %w", err)
	}
	if owner != userID {
		return ErrTransactionNotOwned
	}
	return nil
}

// AttachTags inserts (transaction_id, tag_id) rows. Idempotent — already-
// attached tags are no-ops via ON CONFLICT DO NOTHING.
func (s *Store) AttachTags(ctx context.Context, txID, userID uuid.UUID, tagIDs []uuid.UUID) error {
	for _, tagID := range tagIDs {
		_, err := s.db.Exec(ctx, `
			INSERT INTO transaction_tags (transaction_id, tag_id, created_by_user_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (transaction_id, tag_id) DO NOTHING`,
			txID, tagID, userID)
		if err != nil {
			return fmt.Errorf("attach tag %s: %w", tagID, err)
		}
	}
	return nil
}

// DetachTag removes a single (transaction_id, tag_id) row.
func (s *Store) DetachTag(ctx context.Context, txID, tagID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM transaction_tags WHERE transaction_id = $1 AND tag_id = $2`,
		txID, tagID)
	if err != nil {
		return fmt.Errorf("detach tag: %w", err)
	}
	return nil
}

// TagsForTransaction returns all tags attached to a single transaction.
func (s *Store) TagsForTransaction(ctx context.Context, txID, userID uuid.UUID) ([]Tag, error) {
	q := `SELECT t.id, t.user_id, t.name, t.icon_code, t.created_at, t.updated_at, 0
		FROM tags t
		JOIN transaction_tags tt ON tt.tag_id = t.id
		WHERE tt.transaction_id = $1 AND t.user_id = $2
		ORDER BY LOWER(t.name)`
	rows, err := s.db.Query(ctx, q, txID, userID)
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	defer rows.Close()
	out := make([]Tag, 0)
	for rows.Next() {
		t, err := scanTagWithCount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// VerifyOwnership returns ErrTagNotFound if any of the given tag ids isn't
// owned by the user. Used by AttachTags before the inserts.
func (s *Store) VerifyOwnership(ctx context.Context, userID uuid.UUID, tagIDs []uuid.UUID) error {
	if len(tagIDs) == 0 {
		return nil
	}
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM tags WHERE user_id = $1 AND id = ANY($2)`,
		userID, tagIDs).Scan(&n)
	if err != nil {
		return fmt.Errorf("ownership check: %w", err)
	}
	if n != len(tagIDs) {
		return ErrTagNotFound
	}
	return nil
}

func (s *Store) Update(ctx context.Context, userID, id uuid.UUID, name *string, iconCode *shared.IconCode) (*Tag, error) {
	q := `UPDATE tags SET updated_by_user_id = $1`
	args := []any{userID}
	if name != nil {
		args = append(args, *name)
		q += fmt.Sprintf(", name = $%d", len(args))
	}
	if iconCode != nil {
		b, err := json.Marshal(iconCode)
		if err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
		args = append(args, b)
		q += fmt.Sprintf(", icon_code = $%d::jsonb", len(args))
	}
	args = append(args, id)
	q += fmt.Sprintf(" WHERE id = $%d AND user_id = $1 RETURNING ", len(args)) + tagColumns

	t, err := scanTag(s.db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTagNotFound
	}
	if err != nil {
		return nil, mapInsertError(err)
	}
	return t, nil
}

func (s *Store) Delete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM tags WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("db error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTagNotFound
	}
	return nil
}

func mapInsertError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrTagExists
	}
	return fmt.Errorf("db error: %w", err)
}
