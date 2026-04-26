package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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
	password_hash, currency, avatar_url, status, created_at, updated_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.Username, &u.Email, &u.EmailVerifiedAt, &u.DisplayName,
		&u.PasswordHash, &u.Currency, &u.AvatarURL, &u.Status, &u.CreatedAt, &u.UpdatedAt,
	)
	return &u, err
}

// CreateUser inserts users + user_preferences in one transaction.
// Auto-creates the preferences row with Phase 0 defaults per phase0/be.md.
func (s *Store) CreateUser(ctx context.Context, u *User) (*User, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate user id: %w", err)
	}
	u.ID = id

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	insertUser := `
		INSERT INTO users (id, username, email, display_name, password_hash, currency, avatar_url, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'active')
		RETURNING ` + userColumns

	created, err := scanUser(tx.QueryRow(ctx, insertUser,
		u.ID, u.Username, u.Email, u.DisplayName, u.PasswordHash, u.Currency, u.AvatarURL,
	))
	if err != nil {
		return nil, mapInsertError(err)
	}

	insertPrefs := `
		INSERT INTO user_preferences (user_id, preferences)
		VALUES ($1, $2::jsonb)`
	defaults := `{"timezone":"Asia/Bangkok","theme":"system","language":"th"}`
	if _, err := tx.Exec(ctx, insertPrefs, created.ID, defaults); err != nil {
		return nil, fmt.Errorf("insert user_preferences: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	return created, nil
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
