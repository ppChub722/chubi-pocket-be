package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

// NewStore initializes the repository
func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

var (
	ErrUserNotFound   = errors.New("user not found")
	ErrDuplicateEntry = errors.New("username or email already exists")
)

// CreateUser inserts a new user into the database
func (s *Store) CreateUser(ctx context.Context, username, email, passwordHash, currency string) (*User, error) {
	if currency == "" {
		currency = "THB"
	}

	newUser := &User{
		Username:     username,
		Email:        email,
		PasswordHash: passwordHash,
		Currency:     currency,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	query := `
		INSERT INTO users (username, email, password_hash, currency, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`

	err := s.db.QueryRow(ctx, query,
		newUser.Username,
		newUser.Email,
		newUser.PasswordHash,
		newUser.Currency,
		newUser.CreatedAt,
		newUser.UpdatedAt,
	).Scan(&newUser.ID)

	if err != nil {
		// Postgres Error 23505 = Unique Violation (Duplicate)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDuplicateEntry
		}
		return nil, fmt.Errorf("db error: %w", err)
	}

	return newUser, nil
}

// GetUserByIdentifier finds a user by Username OR Email (for login)
func (s *Store) GetUserByIdentifier(ctx context.Context, identifier string) (*User, error) {
	query := `
		SELECT id, username, email, password_hash, currency, avatar_url, created_at, updated_at
		FROM users
		WHERE email = $1 OR username = $1
	`

	var user User
	err := s.db.QueryRow(ctx, query, identifier).Scan(
		&user.ID,
		&user.Username,
		&user.Email,
		&user.PasswordHash,
		&user.Currency,
		&user.AvatarURL,
		&user.CreatedAt,
		&user.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("db error: %w", err)
	}

	return &user, nil
}

// GetUserByID finds a user by ID
func (s *Store) GetUserByID(ctx context.Context, userID int64) (*User, error) {
	query := `
		SELECT id, username, email, password_hash, currency, avatar_url, created_at, updated_at
		FROM users
		WHERE id = $1
	`

	var user User
	err := s.db.QueryRow(ctx, query, userID).Scan(
		&user.ID,
		&user.Username,
		&user.Email,
		&user.PasswordHash,
		&user.Currency,
		&user.AvatarURL,
		&user.CreatedAt,
		&user.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("db error: %w", err)
	}

	return &user, nil
}

// UpdateUser updates user profile fields
func (s *Store) UpdateUser(ctx context.Context, userID int64, username *string, currency *string, avatarURL *string) (*User, error) {
	query := `
		UPDATE users SET
			username = COALESCE($2, username),
			currency = COALESCE($3, currency),
			avatar_url = COALESCE($4, avatar_url),
			updated_at = NOW()
		WHERE id = $1
		RETURNING id, username, email, password_hash, currency, avatar_url, created_at, updated_at
	`

	var user User
	err := s.db.QueryRow(ctx, query, userID, username, currency, avatarURL).Scan(
		&user.ID,
		&user.Username,
		&user.Email,
		&user.PasswordHash,
		&user.Currency,
		&user.AvatarURL,
		&user.CreatedAt,
		&user.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("db error: %w", err)
	}

	return &user, nil
}

// UpdatePassword updates the user's password hash
func (s *Store) UpdatePassword(ctx context.Context, userID int64, newPasswordHash string) error {
	query := `UPDATE users SET password_hash = $2, updated_at = NOW() WHERE id = $1`
	tag, err := s.db.Exec(ctx, query, userID, newPasswordHash)
	if err != nil {
		return fmt.Errorf("db error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}
