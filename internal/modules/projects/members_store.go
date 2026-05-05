package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const memberColumns = `id, project_id, user_id, display_name, role, status, icon_code,
	invited_at, joined_at, left_at, created_at, updated_at`

func scanMember(row pgx.Row) (*ProjectMember, error) {
	var m ProjectMember
	var iconBytes []byte
	err := row.Scan(
		&m.ID, &m.ProjectID, &m.UserID, &m.DisplayName, &m.Role, &m.Status, &iconBytes,
		&m.InvitedAt, &m.JoinedAt, &m.LeftAt, &m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if iconBytes != nil {
		m.IconCode = new(shared.IconCode)
		if err := json.Unmarshal(iconBytes, m.IconCode); err != nil {
			return nil, fmt.Errorf("unmarshal icon_code: %w", err)
		}
	}
	return &m, nil
}

// InsertMemberTx is the unified insert used by all AddMember variants and
// by the create-project owner-row insert. Caller decides values based on
// variant. Post-migration 27, members have only two kinds: linked
// (userID set) or ad-hoc (userID nil).
func (s *Store) InsertMemberTx(
	ctx context.Context, tx pgx.Tx,
	projectID uuid.UUID, userID *uuid.UUID,
	displayName, role, status string, createdBy uuid.UUID,
	iconCode *shared.IconCode,
) (*ProjectMember, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	// invited_at / joined_at derived from status. We compute them in Go
	// (rather than CASE expressions on $7 status) because Postgres rejects
	// same-positional-param across mixed contexts with SQLSTATE 42P08
	// "inconsistent types deduced for parameter".
	now := time.Now()
	var invitedAt, joinedAt *time.Time
	switch status {
	case "pending":
		invitedAt = &now
	case "active":
		joinedAt = &now
	}
	var iconJSON []byte
	if iconCode != nil {
		if iconJSON, err = json.Marshal(iconCode); err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
	}
	q := `INSERT INTO project_members
		(id, project_id, user_id, display_name, role, status, icon_code,
		 invited_at, joined_at, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $10)
		RETURNING ` + memberColumns
	m, err := scanMember(tx.QueryRow(ctx, q,
		id, projectID, userID, displayName, role, status, iconJSON,
		invitedAt, joinedAt, createdBy,
	))
	if err != nil {
		// Translate the unique-violation on the partial (project_id, user_id) index.
		if isUniqueViolation(err) {
			return nil, ErrMemberAlreadyExists
		}
		return nil, fmt.Errorf("insert member: %w", err)
	}
	return m, nil
}

// GetMember loads a single member row.
func (s *Store) GetMember(ctx context.Context, memberID uuid.UUID) (*ProjectMember, error) {
	q := `SELECT ` + memberColumns + ` FROM project_members WHERE id = $1`
	m, err := scanMember(s.db.QueryRow(ctx, q, memberID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return m, nil
}

// GetMemberTx is the tx-aware variant — locks the row for update.
func (s *Store) GetMemberTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) (*ProjectMember, error) {
	q := `SELECT ` + memberColumns + ` FROM project_members WHERE id = $1 FOR UPDATE`
	m, err := scanMember(tx.QueryRow(ctx, q, memberID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return m, nil
}

// IsActiveMember reports whether userID has an active member row in projectID.
func (s *Store) IsActiveMember(ctx context.Context, projectID, userID uuid.UUID) (bool, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM project_members
		WHERE project_id = $1 AND user_id = $2 AND status = 'active'`,
		projectID, userID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("is member: %w", err)
	}
	return n > 0, nil
}

func (s *Store) ListMembers(ctx context.Context, projectID uuid.UUID) ([]ProjectMember, error) {
	q := `SELECT ` + memberColumns + ` FROM project_members
		WHERE project_id = $1
		ORDER BY (role = 'owner') DESC, status, LOWER(display_name)`
	rows, err := s.db.Query(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	out := make([]ProjectMember, 0)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *Store) UpdateMemberRole(
	ctx context.Context, projectID, memberID uuid.UUID, role string, actorUserID uuid.UUID,
) (*ProjectMember, error) {
	q := `UPDATE project_members SET role = $1, updated_by_user_id = $2
		WHERE id = $3 AND project_id = $4
		RETURNING ` + memberColumns
	m, err := scanMember(s.db.QueryRow(ctx, q, role, actorUserID, memberID, projectID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update role: %w", err)
	}
	return m, nil
}

func (s *Store) UpdateMemberIconCode(
	ctx context.Context, projectID, memberID uuid.UUID, iconCode *shared.IconCode, actorUserID uuid.UUID,
) (*ProjectMember, error) {
	iconJSON, err := json.Marshal(iconCode)
	if err != nil {
		return nil, fmt.Errorf("marshal icon_code: %w", err)
	}
	q := `UPDATE project_members SET icon_code = $1::jsonb, updated_by_user_id = $2
		WHERE id = $3 AND project_id = $4
		RETURNING ` + memberColumns
	m, err := scanMember(s.db.QueryRow(ctx, q, iconJSON, actorUserID, memberID, projectID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update icon_code: %w", err)
	}
	return m, nil
}

// SoftRemoveMember marks status='left' and stamps left_at. Used by both
// owner removal and member self-leave.
func (s *Store) SoftRemoveMember(
	ctx context.Context, projectID, memberID uuid.UUID, actorUserID uuid.UUID,
) (*ProjectMember, error) {
	q := `UPDATE project_members SET status = 'left', left_at = NOW(),
		updated_by_user_id = $1
		WHERE id = $2 AND project_id = $3
		RETURNING ` + memberColumns
	m, err := scanMember(s.db.QueryRow(ctx, q, actorUserID, memberID, projectID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("soft remove: %w", err)
	}
	return m, nil
}

// LinkMemberToUserTx populates user_id, sets joined_at, flips status to
// 'active'. Inside the link-request accept tx.
func (s *Store) LinkMemberToUserTx(
	ctx context.Context, tx pgx.Tx, memberID, newUserID uuid.UUID,
) (*ProjectMember, error) {
	q := `UPDATE project_members SET
		user_id = $1, status = 'active', joined_at = COALESCE(joined_at, NOW()),
		updated_by_user_id = $1
		WHERE id = $2
		RETURNING ` + memberColumns
	m, err := scanMember(tx.QueryRow(ctx, q, newUserID, memberID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrMemberAlreadyExists
		}
		return nil, fmt.Errorf("link member: %w", err)
	}
	return m, nil
}

func isUniqueViolation(err error) bool {
	// Avoid pgconn import here; cheap string match works for our purposes.
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "duplicate key") || contains(msg, "23505") ||
		contains(msg, "idx_project_members_user")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
