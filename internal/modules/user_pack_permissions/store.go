package user_pack_permissions

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

// ListForUser returns all non-expired pack permissions for the user.
// Expired rows (expires_at IS NOT NULL AND expires_at < NOW()) are excluded.
func (s *Store) ListForUser(ctx context.Context, userID uuid.UUID) ([]UserPackPermission, error) {
	q := `SELECT id, user_id, pack_id, granted_at, expires_at
		FROM user_pack_permissions
		WHERE user_id = $1
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY granted_at ASC`
	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list pack permissions: %w", err)
	}
	defer rows.Close()
	out := make([]UserPackPermission, 0)
	for rows.Next() {
		var p UserPackPermission
		if err := rows.Scan(&p.ID, &p.UserID, &p.PackID, &p.GrantedAt, &p.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan pack permission: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
