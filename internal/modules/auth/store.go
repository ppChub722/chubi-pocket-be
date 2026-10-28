package auth

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
	ErrUserNotFound   = errors.New("user not found")
	ErrUsernameExists = errors.New("username already exists")
	ErrEmailExists    = errors.New("email already exists")
)

const userColumns = `id, username, email, email_verified_at, display_name,
	password_hash, currency, icon_code, status, created_at, updated_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	var iconBytes []byte
	err := row.Scan(
		&u.ID, &u.Username, &u.Email, &u.EmailVerifiedAt, &u.DisplayName,
		&u.PasswordHash, &u.Currency, &iconBytes, &u.Status, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if iconBytes != nil {
		u.IconCode = new(shared.IconCode)
		if err := json.Unmarshal(iconBytes, u.IconCode); err != nil {
			return nil, fmt.Errorf("unmarshal icon_code: %w", err)
		}
	}
	return &u, nil
}

func (s *Store) Pool() *pgxpool.Pool { return s.db }

func (s *Store) InsertUserTx(ctx context.Context, tx pgx.Tx, u *User) (*User, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate user id: %w", err)
	}
	u.ID = id

	q := `
		INSERT INTO users (id, username, email, display_name, password_hash, currency, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'active')
		RETURNING ` + userColumns
	created, err := scanUser(tx.QueryRow(ctx, q,
		u.ID, u.Username, u.Email, u.DisplayName, u.PasswordHash, u.Currency,
	))
	if err != nil {
		return nil, mapInsertError(err)
	}
	return created, nil
}

func (s *Store) InsertPreferencesTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	// 0.4.1-2 P2: no theme / language: the app sends the device's values once after login.
	const defaults = `{"timezone":"Asia/Bangkok","money_style":"color_only"}`
	if _, err := tx.Exec(ctx,
		`INSERT INTO user_preferences (user_id, preferences) VALUES ($1, $2::jsonb)`,
		userID, defaults); err != nil {
		return fmt.Errorf("insert user_preferences: %w", err)
	}
	return nil
}

func mapInsertError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		if strings.Contains(pgErr.ConstraintName, "username") {
			return ErrUsernameExists
		}
		if strings.Contains(pgErr.ConstraintName, "email") {
			return ErrEmailExists
		}
	}
	return fmt.Errorf("db error: %w", err)
}

func (s *Store) GetByIdentifier(ctx context.Context, identifier string) (*User, error) {
	q := `SELECT ` + userColumns + ` FROM users WHERE username = $1 OR email = $1 LIMIT 1`
	u, err := scanUser(s.db.QueryRow(ctx, q, identifier))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return u, nil
}

func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	q := `SELECT ` + userColumns + ` FROM users WHERE id = $1`
	u, err := scanUser(s.db.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return u, nil
}

// StatusByID is all the auth middleware needs of the caller: whether the account is
// active (one narrow read on every request, never the whole row).
func (s *Store) StatusByID(ctx context.Context, id uuid.UUID) (string, error) {
	var status string
	err := s.db.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUserNotFound
	}
	if err != nil {
		return "", fmt.Errorf("db error: %w", err)
	}
	return status, nil
}

func (s *Store) UpdatePassword(ctx context.Context, id uuid.UUID, newHash string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_by_user_id = $1 WHERE id = $1`,
		id, newHash)
	if err != nil {
		return fmt.Errorf("db error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}
