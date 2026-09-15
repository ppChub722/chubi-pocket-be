package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrMemberNotFound    = errors.New("account member not found")
	ErrAlreadyMember     = errors.New("user is already a member of this account")
	ErrNotMember         = errors.New("caller is not an active member of this account")
	ErrNotOwner          = errors.New("only the account owner can perform this action")
	ErrUserNotFound      = errors.New("no user matches that email")
	ErrOwnerMustTransfer = errors.New("owner must transfer ownership before leaving while other active members exist")
	ErrAccountHasMembers = errors.New("account has other active members; remove them or transfer ownership first")
	ErrScopeNotAllowed   = errors.New("ex-members may only use report_scope 'none' or 'own'")
)

const memberColumns = `id, account_id, user_id, role, report_scope,
	joined_at, left_at, created_at, updated_at`

func scanAccountMember(row pgx.Row) (*AccountMember, error) {
	var m AccountMember
	err := row.Scan(
		&m.ID, &m.AccountID, &m.UserID, &m.Role, &m.ReportScope,
		&m.JoinedAt, &m.LeftAt, &m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// InsertMemberTx inserts a membership row. status is derived from
// joinedAt: nil = pending invite, set = active (used by the owner
// auto-row on account create and by the migration-parity path).
func (s *Store) InsertMemberTx(
	ctx context.Context, tx pgx.Tx,
	accountID, userID uuid.UUID, role, reportScope string,
	joinedAt *time.Time, createdBy uuid.UUID,
) (*AccountMember, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	q := `INSERT INTO account_members
		(id, account_id, user_id, role, report_scope, joined_at,
		 created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		RETURNING ` + memberColumns
	m, err := scanAccountMember(tx.QueryRow(ctx, q,
		id, accountID, userID, role, reportScope, joinedAt, createdBy,
	))
	if err != nil {
		if isMemberUniqueViolation(err) {
			return nil, ErrAlreadyMember
		}
		return nil, fmt.Errorf("insert account member: %w", err)
	}
	return m, nil
}

// GetMember loads a single membership row by id.
func (s *Store) GetMember(ctx context.Context, memberID uuid.UUID) (*AccountMember, error) {
	q := `SELECT ` + memberColumns + ` FROM account_members WHERE id = $1`
	m, err := scanAccountMember(s.db.QueryRow(ctx, q, memberID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return m, nil
}

// GetMemberTx is the tx-aware variant — locks the row for update.
func (s *Store) GetMemberTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) (*AccountMember, error) {
	q := `SELECT ` + memberColumns + ` FROM account_members WHERE id = $1 FOR UPDATE`
	m, err := scanAccountMember(tx.QueryRow(ctx, q, memberID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return m, nil
}

// IsActiveMember reports whether userID holds an active (joined, not
// left) membership on accountID.
func (s *Store) IsActiveMember(ctx context.Context, accountID, userID uuid.UUID) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx,
		`SELECT `+shared.ActiveMembershipPredicate("$1", "$2"),
		accountID, userID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("is member: %w", err)
	}
	return ok, nil
}

// HasLiveMembership reports whether userID has any not-left row
// (pending OR active) on accountID — the duplicate-invite gate.
func (s *Store) HasLiveMembership(ctx context.Context, accountID, userID uuid.UUID) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM account_members
			WHERE account_id = $1 AND user_id = $2 AND left_at IS NULL
		)`, accountID, userID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("live membership: %w", err)
	}
	return ok, nil
}

// CountActiveMembers returns the number of active members on the account.
func (s *Store) CountActiveMembers(ctx context.Context, accountID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM account_members
		WHERE account_id = $1 AND joined_at IS NOT NULL AND left_at IS NULL`,
		accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count members: %w", err)
	}
	return n, nil
}

// CountActiveMembersTx — same, inside a caller tx.
func (s *Store) CountActiveMembersTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM account_members
		WHERE account_id = $1 AND joined_at IS NOT NULL AND left_at IS NULL`,
		accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count members: %w", err)
	}
	return n, nil
}

// GoverningMembership resolves the row whose report_scope governs
// userID's aggregates on accountID: the active row when one exists,
// else the most recent left row. Pending invites never count. Mirrors
// shared.ReportScopePredicate's inner ORDER BY exactly.
func (s *Store) GoverningMembership(ctx context.Context, accountID, userID uuid.UUID) (*AccountMember, error) {
	q := `SELECT ` + memberColumns + ` FROM account_members
		WHERE account_id = $1 AND user_id = $2 AND joined_at IS NOT NULL
		ORDER BY (left_at IS NULL) DESC, joined_at DESC
		LIMIT 1`
	m, err := scanAccountMember(s.db.QueryRow(ctx, q, accountID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return m, nil
}

// UpdateReportScope sets report_scope on one membership row.
func (s *Store) UpdateReportScope(
	ctx context.Context, memberID uuid.UUID, scope string, actorUserID uuid.UUID,
) (*AccountMember, error) {
	q := `UPDATE account_members SET report_scope = $1, updated_by_user_id = $2
		WHERE id = $3
		RETURNING ` + memberColumns
	m, err := scanAccountMember(s.db.QueryRow(ctx, q, scope, actorUserID, memberID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update report_scope: %w", err)
	}
	return m, nil
}

// SoftRemoveMember stamps left_at (never hard-deletes; spec §14/2 rule
// 4) and caps report_scope at 'own' — ex-members lose the 'all' option
// (spec §14/5).
func (s *Store) SoftRemoveMember(
	ctx context.Context, accountID, memberID uuid.UUID, actorUserID uuid.UUID,
) (*AccountMember, error) {
	q := `UPDATE account_members SET left_at = NOW(),
		report_scope = CASE WHEN report_scope = 'all' THEN 'own' ELSE report_scope END,
		updated_by_user_id = $1
		WHERE id = $2 AND account_id = $3 AND left_at IS NULL
		RETURNING ` + memberColumns
	m, err := scanAccountMember(s.db.QueryRow(ctx, q, actorUserID, memberID, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("soft remove: %w", err)
	}
	return m, nil
}

// ActivateMemberTx stamps joined_at on a pending row (invite accept).
// New members always start at report_scope='none' (spec §14/4).
func (s *Store) ActivateMemberTx(
	ctx context.Context, tx pgx.Tx, memberID, actorUserID uuid.UUID,
) (*AccountMember, error) {
	q := `UPDATE account_members SET
		joined_at = NOW(), report_scope = 'none', updated_by_user_id = $1
		WHERE id = $2 AND joined_at IS NULL AND left_at IS NULL
		RETURNING ` + memberColumns
	m, err := scanAccountMember(tx.QueryRow(ctx, q, actorUserID, memberID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("activate member: %w", err)
	}
	return m, nil
}

// ResetScopesToNoneTx is the conversion auto tick-out (spec §14/4): the
// moment a wallet becomes shared, report_scope drops to 'none' for
// EVERY live member (including the owner). Each member can turn it back
// on for themselves afterwards.
func (s *Store) ResetScopesToNoneTx(
	ctx context.Context, tx pgx.Tx, accountID, actorUserID uuid.UUID,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE account_members SET report_scope = 'none', updated_by_user_id = $1
		WHERE account_id = $2 AND left_at IS NULL AND report_scope <> 'none'`,
		actorUserID, accountID); err != nil {
		return fmt.Errorf("reset scopes: %w", err)
	}
	return nil
}

// VoidPendingMemberTx stamps left_at on a still-pending row (invite
// reject / cancel) so a later re-invite passes the live-unique index.
func (s *Store) VoidPendingMemberTx(
	ctx context.Context, tx pgx.Tx, memberID, actorUserID uuid.UUID,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE account_members SET left_at = NOW(), updated_by_user_id = $1
		WHERE id = $2 AND joined_at IS NULL AND left_at IS NULL`,
		actorUserID, memberID); err != nil {
		return fmt.Errorf("void pending member: %w", err)
	}
	return nil
}

// TransferOwnershipTx swaps role between the current owner's and the new
// owner's active member rows AND repoints accounts.user_id — the account
// entity and the membership ledger must agree on who owns the wallet.
func (s *Store) TransferOwnershipTx(
	ctx context.Context, tx pgx.Tx, accountID, currentOwner, newOwner uuid.UUID,
) error {
	// Demote current owner's active row.
	if _, err := tx.Exec(ctx, `
		UPDATE account_members SET role = 'member', updated_by_user_id = $1
		WHERE account_id = $2 AND user_id = $1 AND role = 'owner' AND left_at IS NULL`,
		currentOwner, accountID); err != nil {
		return fmt.Errorf("demote owner: %w", err)
	}
	// Promote the target's active row.
	tag, err := tx.Exec(ctx, `
		UPDATE account_members SET role = 'owner', updated_by_user_id = $1
		WHERE account_id = $2 AND user_id = $3
		  AND joined_at IS NOT NULL AND left_at IS NULL`,
		currentOwner, accountID, newOwner)
	if err != nil {
		return fmt.Errorf("promote owner: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMemberNotFound
	}
	// Repoint the account entity.
	if _, err := tx.Exec(ctx, `
		UPDATE accounts SET user_id = $1, updated_by_user_id = $2
		WHERE id = $3`,
		newOwner, currentOwner, accountID); err != nil {
		return fmt.Errorf("update account owner: %w", err)
	}
	return nil
}

// ListMembersByAccount returns the full membership history of one account
// for the members screen: active members first (owner leading), then
// pending invites, then past members (most recent departure first).
// Voided invites (joined_at NULL + left_at set — an invite that was
// rejected or withdrawn, never an actual member) are excluded.
func (s *Store) ListMembersByAccount(ctx context.Context, accountID uuid.UUID) ([]MemberHistoryView, error) {
	rows, err := s.db.Query(ctx, `
		SELECT am.id, am.user_id, u.display_name, u.icon_code, am.role,
		       am.joined_at, am.left_at
		FROM account_members am
		JOIN users u ON u.id = am.user_id
		WHERE am.account_id = $1
		  AND NOT (am.joined_at IS NULL AND am.left_at IS NOT NULL)
		ORDER BY (am.left_at IS NOT NULL) ASC,
		         (am.joined_at IS NULL) ASC,
		         (am.role = 'owner') DESC,
		         am.joined_at ASC NULLS LAST,
		         am.left_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list account members: %w", err)
	}
	defer rows.Close()

	out := []MemberHistoryView{}
	for rows.Next() {
		var (
			m         MemberHistoryView
			iconBytes []byte
		)
		if err := rows.Scan(&m.ID, &m.UserID, &m.DisplayName, &iconBytes,
			&m.Role, &m.JoinedAt, &m.LeftAt); err != nil {
			return nil, fmt.Errorf("scan member history: %w", err)
		}
		if iconBytes != nil {
			m.IconCode = new(shared.IconCode)
			if err := json.Unmarshal(iconBytes, m.IconCode); err != nil {
				return nil, fmt.Errorf("unmarshal member icon_code: %w", err)
			}
		}
		switch {
		case m.LeftAt != nil:
			m.Status = "left"
		case m.JoinedAt == nil:
			m.Status = "pending"
		default:
			m.Status = "active"
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// fillMembers batch-loads the active members[] + my_report_scope +
// is_shared for a page of accounts (pinned contract on GET /v1/accounts).
// Single query; no N+1 against the page.
func (s *Store) fillMembers(ctx context.Context, callerID uuid.UUID, accs []Account) error {
	for i := range accs {
		accs[i].Members = []MemberView{}
		accs[i].MyReportScope = ReportScopeNone
	}
	if len(accs) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(accs))
	idx := make(map[uuid.UUID]int, len(accs))
	for i := range accs {
		ids[i] = accs[i].ID
		idx[accs[i].ID] = i
	}
	rows, err := s.db.Query(ctx, `
		SELECT am.account_id, am.id, am.user_id, u.display_name, u.icon_code,
		       am.role, am.joined_at, am.report_scope
		FROM account_members am
		JOIN users u ON u.id = am.user_id
		WHERE am.account_id = ANY($1)
		  AND am.joined_at IS NOT NULL AND am.left_at IS NULL
		ORDER BY am.account_id, (am.role = 'owner') DESC, am.joined_at ASC`, ids)
	if err != nil {
		return fmt.Errorf("load members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			accountID uuid.UUID
			m         MemberView
			iconBytes []byte
			scope     string
		)
		if err := rows.Scan(&accountID, &m.ID, &m.UserID, &m.DisplayName, &iconBytes,
			&m.Role, &m.JoinedAt, &scope); err != nil {
			return fmt.Errorf("scan member: %w", err)
		}
		if iconBytes != nil {
			m.IconCode = new(shared.IconCode)
			if err := json.Unmarshal(iconBytes, m.IconCode); err != nil {
				return fmt.Errorf("unmarshal member icon_code: %w", err)
			}
		}
		i, ok := idx[accountID]
		if !ok {
			continue
		}
		accs[i].Members = append(accs[i].Members, m)
		if m.UserID == callerID {
			accs[i].MyReportScope = scope
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range accs {
		accs[i].IsShared = len(accs[i].Members) > 1
	}
	return nil
}

func isMemberUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "23505") ||
		strings.Contains(msg, "idx_account_members_live_unique")
}
