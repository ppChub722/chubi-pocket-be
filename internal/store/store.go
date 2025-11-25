package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ppChub722/finna-bbear-be/internal/models"
)

// Store defines the interface for database operations.

type Store struct {
	DB *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store {
	return &Store{DB: db}
}

// CreateUser creates a new user in the database.
func (s *Store) CreateUser(ctx context.Context, user *models.User) error {
	query := `INSERT INTO users (username, email, password) VALUES ($1, $2, $3) RETURNING id, created_at, updated_at`
	return s.DB.QueryRow(ctx, query, user.Username, user.Email, user.Password).Scan(&user.ID, &user.CreatedAt, &user.UpdatedAt)
}

// GetUserByEmail retrieves a user by their email address.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	query := `SELECT id, username, email, password, created_at, updated_at FROM users WHERE email = $1`
	user := &models.User{}
	err := s.DB.QueryRow(ctx, query, email).Scan(&user.ID, &user.Username, &user.Email, &user.Password, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// BlacklistToken adds a token to the blacklist.
func (s *Store) BlacklistToken(ctx context.Context, token string, expiresAt time.Time) error {
	query := `INSERT INTO token_blacklist (token, expires_at) VALUES ($1, $2)`
	_, err := s.DB.Exec(ctx, query, token, expiresAt)
	return err
}
