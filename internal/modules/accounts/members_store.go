package accounts

import (
	"context"
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
	ErrInviteTarget      = errors.New("send exactly one of contact_id or email")
	ErrContactNotFound   = errors.New("contact not found")
	ErrContactArchived   = errors.New("contact is archived")
	ErrContactNotLinked  = errors.New("this contact isn't linked to an app account")
	ErrOwnerMustTransfer = errors.New("owner must transfer ownership before leaving while other active members exist")
	ErrAccountHasMembers = errors.New("account has other active members; remove them or transfer ownership first")
	ErrScopeNotAllowed   = errors.New("ex-members may only use report_scope 'none' or 'own'")
	ErrNotInvite         = errors.New("notification is not an account_invite")
	ErrInvalidInclude    = errors.New("include must be summary and/or members")
	ErrInvalidDate       = errors.New("from / to must be YYYY-MM-DD")
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
		(id, account_id, user_id, role, report_scope, joined_at, sort_order,
		 created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, ` + nextMemberSortOrder("$3") + `, $7, $7)
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

func (s *Store) ActivateMemberTx(
	ctx context.Context, tx pgx.Tx, memberID, actorUserID uuid.UUID,
) (*AccountMember, error) {
	q := `UPDATE account_members SET
		joined_at = NOW(), report_scope = 'none', updated_by_user_id = $1,
		sort_order = ` + nextMemberSortOrder("account_members.user_id") + `
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

func (s *Store) TransferOwnershipTx(
	ctx context.Context, tx pgx.Tx, accountID, currentOwner, newOwner uuid.UUID,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE account_members SET role = 'member', updated_by_user_id = $1
		WHERE account_id = $2 AND user_id = $1 AND role = 'owner' AND left_at IS NULL`,
		currentOwner, accountID); err != nil {
		return fmt.Errorf("demote owner: %w", err)
	}
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
	if _, err := tx.Exec(ctx, `
		UPDATE accounts SET user_id = $1, updated_by_user_id = $2
		WHERE id = $3`,
		newOwner, currentOwner, accountID); err != nil {
		return fmt.Errorf("update account owner: %w", err)
	}
	return nil
}

func (s *Store) ListMembersByAccount(ctx context.Context, callerID, accountID uuid.UUID) ([]MemberHistoryView, error) {
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
		if err := rows.Scan(&m.ID, &m.UserID, &m.Name, &iconBytes,
			&m.Role, &m.JoinedAt, &m.LeftAt); err != nil {
			return nil, fmt.Errorf("scan member history: %w", err)
		}
		m.IconCode = shared.IconFromJSON(iconBytes)
		m.IsMe = m.UserID == callerID
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

func (s *Store) fillMembers(ctx context.Context, callerID uuid.UUID, accs []Account) error {
	for i := range accs {
		accs[i].Members = []MemberView{}
		accs[i].MyReportScope = ReportScopeNone
		accs[i].MyRole = MemberRoleMember
		accs[i].CanEdit = false
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
		       am.role, am.joined_at, am.report_scope, am.sort_order
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
			sortOrder int
		)
		if err := rows.Scan(&accountID, &m.ID, &m.UserID, &m.Name, &iconBytes,
			&m.Role, &m.JoinedAt, &scope, &sortOrder); err != nil {
			return fmt.Errorf("scan member: %w", err)
		}
		m.IconCode = shared.IconFromJSON(iconBytes)
		m.IsMe = m.UserID == callerID
		i, ok := idx[accountID]
		if !ok {
			continue
		}
		accs[i].Members = append(accs[i].Members, m)
		if m.UserID == callerID {
			accs[i].MyReportScope = scope
			accs[i].MyRole = m.Role
			accs[i].CanEdit = m.Role == MemberRoleOwner
			accs[i].SortOrder = sortOrder
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range accs {
		accs[i].IsShared = len(accs[i].Members) > 1
	}
	savings, err := shared.LoadWalletSavings(ctx, s.db, callerID, ids)
	if err != nil {
		return err
	}
	for i := range accs {
		accs[i].Savings = savings[accs[i].ID]
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

func nextMemberSortOrder(userExpr string) string {
	return fmt.Sprintf(`COALESCE((SELECT MAX(x.sort_order) + 1 FROM account_members x
		WHERE x.user_id = %s AND x.left_at IS NULL), 0)`, userExpr)
}
