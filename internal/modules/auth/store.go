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

// Pool exposes the underlying connection pool to the Service so it can
// orchestrate multi-store transactions (register flow composes user insert,
// preferences insert, and the categories seed in one tx).
func (s *Store) Pool() *pgxpool.Pool { return s.db }

// InsertUserTx inserts the users row inside caller's tx. Does NOT insert
// user_preferences — Service.Register composes that and the categories seed.
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

// InsertPreferencesTx inserts the user_preferences row inside caller's tx
// with Phase 0 defaults. created_by_user_id is left NULL (system-created).
func (s *Store) InsertPreferencesTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	const defaults = `{"timezone":"Asia/Bangkok","theme":"system","language":"th"}`
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
		// Constraint name distinguishes which uniqueness was violated.
		// Index names from migration: idx_users_username, idx_users_email
		if strings.Contains(pgErr.ConstraintName, "username") {
			return ErrUsernameExists
		}
		if strings.Contains(pgErr.ConstraintName, "email") {
			return ErrEmailExists
		}
	}
	return fmt.Errorf("db error: %w", err)
}

// GetByIdentifier finds a user by username or email (login).
// API layer decides which by `@` presence; store accepts both.
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
