package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

func (s *Store) Pool() *pgxpool.Pool { return s.db }

var (
	ErrNotificationNotFound = errors.New("notification not found")
	ErrNotForYou            = errors.New("notification is not addressed to caller")
)

const notificationColumns = `id, recipient_user_id, type, actor_user_id, payload,
	deep_link, read_at, actioned_at, dismissed_at, created_at, updated_at`

func scanNotification(row pgx.Row) (*Notification, error) {
	var n Notification
	var payloadBytes []byte
	err := row.Scan(
		&n.ID, &n.RecipientUserID, &n.Type, &n.ActorUserID, &payloadBytes,
		&n.DeepLink, &n.ReadAt, &n.ActionedAt, &n.DismissedAt,
		&n.CreatedAt, &n.UpdatedAt,
	)
	if err == nil && payloadBytes != nil {
		n.Payload = json.RawMessage(payloadBytes)
	}
	return &n, err
}

// InsertTx inserts one notification row inside the producer's tx. Returns
// the created row's id; the producer rarely needs the full row so we keep
// the call site small.
func (s *Store) InsertTx(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, notifType string,
	actorUserID *uuid.UUID, payload []byte, deepLink *string,
) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("uuid: %w", err)
	}
	q := `INSERT INTO notifications
		(id, recipient_user_id, type, actor_user_id, payload, deep_link, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $4, $4)`
	if _, err := tx.Exec(ctx, q, id, recipientUserID, notifType, actorUserID, payload, deepLink); err != nil {
		return uuid.Nil, fmt.Errorf("insert notification: %w", err)
	}
	return id, nil
}

// List returns the caller's notification rows + unread count + total.
func (s *Store) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]Notification, int, int, error) {
	whereClauses := []string{"recipient_user_id = $1"}
	args := []any{userID}

	switch strings.ToLower(strings.TrimSpace(f.Read)) {
	case "true":
		whereClauses = append(whereClauses, "read_at IS NOT NULL")
	case "false":
		whereClauses = append(whereClauses, "read_at IS NULL")
	}
	switch strings.ToLower(strings.TrimSpace(f.Actioned)) {
	case "true":
		whereClauses = append(whereClauses, "actioned_at IS NOT NULL")
	case "false":
		whereClauses = append(whereClauses, "actioned_at IS NULL")
	}
	if f.Type != "" {
		args = append(args, f.Type)
		whereClauses = append(whereClauses, fmt.Sprintf("type = $%d", len(args)))
	}
	if f.From != nil {
		args = append(args, *f.From)
		whereClauses = append(whereClauses, fmt.Sprintf("created_at >= $%d::timestamptz", len(args)))
	}
	if f.To != nil {
		args = append(args, *f.To)
		whereClauses = append(whereClauses, fmt.Sprintf("created_at <= $%d::timestamptz", len(args)))
	}
	where := strings.Join(whereClauses, " AND ")

	var (
		total       int
		unreadCount int
	)
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM notifications WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, 0, fmt.Errorf("count: %w", err)
	}
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND read_at IS NULL`,
		userID).Scan(&unreadCount); err != nil {
		return nil, 0, 0, fmt.Errorf("unread count: %w", err)
	}

	args = append(args, f.PerPage)
	limitIdx := len(args)
	args = append(args, (f.Page-1)*f.PerPage)
	offsetIdx := len(args)

	q := fmt.Sprintf(`
		SELECT n.id, n.recipient_user_id, n.type, n.actor_user_id, n.payload,
		       n.deep_link, n.read_at, n.actioned_at, n.dismissed_at,
		       n.created_at, n.updated_at,
		       u.display_name
		FROM notifications n
		LEFT JOIN users u ON u.id = n.actor_user_id
		WHERE %s
		ORDER BY n.created_at DESC
		LIMIT $%d OFFSET $%d`, where, limitIdx, offsetIdx)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	out := make([]Notification, 0, f.PerPage)
	for rows.Next() {
		var (
			n            Notification
			payloadBytes []byte
			actorName    *string
		)
		if err := rows.Scan(
			&n.ID, &n.RecipientUserID, &n.Type, &n.ActorUserID, &payloadBytes,
			&n.DeepLink, &n.ReadAt, &n.ActionedAt, &n.DismissedAt,
			&n.CreatedAt, &n.UpdatedAt,
			&actorName,
		); err != nil {
			return nil, 0, 0, fmt.Errorf("scan: %w", err)
		}
		if payloadBytes != nil {
			n.Payload = json.RawMessage(payloadBytes)
		}
		n.ActorDisplayName = actorName
		out = append(out, n)
	}
	return out, total, unreadCount, rows.Err()
}

func (s *Store) GetByID(ctx context.Context, userID, id uuid.UUID) (*Notification, error) {
	q := `SELECT ` + notificationColumns + ` FROM notifications WHERE id = $1`
	n, err := scanNotification(s.db.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotificationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	if n.RecipientUserID != userID {
		return nil, ErrNotForYou
	}
	return n, nil
}

// MarkRead is idempotent — returns the row whether or not it was already
// read. Caller is identified for the audit column.
func (s *Store) MarkRead(ctx context.Context, userID, id uuid.UUID) (*Notification, error) {
	q := `UPDATE notifications SET
		read_at = COALESCE(read_at, NOW()),
		updated_by_user_id = $1
		WHERE id = $2 AND recipient_user_id = $1
		RETURNING ` + notificationColumns
	n, err := scanNotification(s.db.QueryRow(ctx, q, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotificationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mark read: %w", err)
	}
	return n, nil
}

func (s *Store) MarkReadAll(ctx context.Context, userID uuid.UUID) (int, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE notifications SET read_at = NOW(), updated_by_user_id = $1
		WHERE recipient_user_id = $1 AND read_at IS NULL`, userID)
	if err != nil {
		return 0, fmt.Errorf("mark read all: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// MarkActioned implies read.
func (s *Store) MarkActioned(ctx context.Context, userID, id uuid.UUID) (*Notification, error) {
	return s.markTerminal(ctx, userID, id, "actioned_at")
}

// MarkDismissed implies read.
func (s *Store) MarkDismissed(ctx context.Context, userID, id uuid.UUID) (*Notification, error) {
	return s.markTerminal(ctx, userID, id, "dismissed_at")
}

// MarkActionedTx is the tx-aware variant — used by producer modules
// (contacts, projects) when accepting / rejecting a link-request notification
// inside the same tx as the link-side change.
func (s *Store) MarkActionedTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Notification, error) {
	return s.markTerminalTx(ctx, tx, userID, id, "actioned_at")
}

func (s *Store) MarkDismissedTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Notification, error) {
	return s.markTerminalTx(ctx, tx, userID, id, "dismissed_at")
}

func (s *Store) markTerminalTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, col string) (*Notification, error) {
	q := fmt.Sprintf(`UPDATE notifications SET
		%s = COALESCE(%s, NOW()),
		read_at = COALESCE(read_at, NOW()),
		updated_by_user_id = $1
		WHERE id = $2 AND recipient_user_id = $1
		RETURNING %s`, col, col, notificationColumns)
	n, err := scanNotification(tx.QueryRow(ctx, q, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotificationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mark %s in tx: %w", col, err)
	}
	return n, nil
}

// GetByIDForCallerTx fetches a notification under the caller's identity
// inside a tx, returning ErrNotForYou if the row isn't addressed to them.
// Used by accept-link-request flows that need to read the payload before
// applying the link change.
func (s *Store) GetByIDForCallerTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Notification, error) {
	q := `SELECT ` + notificationColumns + ` FROM notifications WHERE id = $1 FOR UPDATE`
	n, err := scanNotification(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotificationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	if n.RecipientUserID != userID {
		return nil, ErrNotForYou
	}
	return n, nil
}

func (s *Store) markTerminal(ctx context.Context, userID, id uuid.UUID, col string) (*Notification, error) {
	q := fmt.Sprintf(`UPDATE notifications SET
		%s = COALESCE(%s, NOW()),
		read_at = COALESCE(read_at, NOW()),
		updated_by_user_id = $1
		WHERE id = $2 AND recipient_user_id = $1
		RETURNING %s`, col, col, notificationColumns)
	n, err := scanNotification(s.db.QueryRow(ctx, q, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotificationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mark %s: %w", col, err)
	}
	return n, nil
}

func (s *Store) Delete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM notifications WHERE id = $1 AND recipient_user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotificationNotFound
	}
	return nil
}

// --- Settings ---

const settingsColumns = `user_id, auto_notify_linked_split_contacts,
	auto_add_to_personal_debt_on_split_notification, auto_record_received_payment,
	auto_resolve_own_in_projects, default_account_id, created_at, updated_at`

func scanSettings(row pgx.Row) (*Settings, error) {
	var s Settings
	err := row.Scan(
		&s.UserID, &s.AutoNotifyLinkedSplitContacts,
		&s.AutoAddToPersonalDebtOnSplitNotification, &s.AutoRecordReceivedPayment,
		&s.AutoResolveOwnInProjects, &s.DefaultAccountID, &s.CreatedAt, &s.UpdatedAt,
	)
	return &s, err
}

func (s *Store) GetSettings(ctx context.Context, userID uuid.UUID) (*Settings, error) {
	q := `SELECT ` + settingsColumns + ` FROM user_notification_settings WHERE user_id = $1`
	row, err := scanSettings(s.db.QueryRow(ctx, q, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		// Defensive: if a user predates the auth-hook seeding and somehow
		// dodged the migration backfill, return defaults rather than 404.
		// The migration's INSERT ... ON CONFLICT covers all known existing
		// users, so this should be unreachable in practice.
		return &Settings{
			UserID:                        userID,
			AutoNotifyLinkedSplitContacts: true,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return row, nil
}

// SeedSettingsTx is the auth registration hook. Called inside the user
// insert tx to atomically create the default settings row.
func (s *Store) SeedSettingsTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO user_notification_settings (user_id, created_by_user_id, updated_by_user_id)
		VALUES ($1, $1, $1)
		ON CONFLICT (user_id) DO NOTHING`, userID)
	if err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	return nil
}

func (s *Store) UpdateSettings(ctx context.Context, userID uuid.UUID, req UpdateSettingsRequest) (*Settings, error) {
	q := `UPDATE user_notification_settings SET updated_by_user_id = $1`
	args := []any{userID}

	if req.AutoNotifyLinkedSplitContacts != nil {
		args = append(args, *req.AutoNotifyLinkedSplitContacts)
		q += fmt.Sprintf(", auto_notify_linked_split_contacts = $%d", len(args))
	}
	if req.AutoAddToPersonalDebtOnSplitNotification != nil {
		args = append(args, *req.AutoAddToPersonalDebtOnSplitNotification)
		q += fmt.Sprintf(", auto_add_to_personal_debt_on_split_notification = $%d", len(args))
	}
	if req.AutoRecordReceivedPayment != nil {
		args = append(args, *req.AutoRecordReceivedPayment)
		q += fmt.Sprintf(", auto_record_received_payment = $%d", len(args))
	}
	if req.AutoResolveOwnInProjects != nil {
		args = append(args, *req.AutoResolveOwnInProjects)
		q += fmt.Sprintf(", auto_resolve_own_in_projects = $%d", len(args))
	}
	if req.DefaultAccountID != nil {
		args = append(args, *req.DefaultAccountID)
		q += fmt.Sprintf(", default_account_id = $%d", len(args))
	}
	q += " WHERE user_id = $1 RETURNING " + settingsColumns

	row, err := scanSettings(s.db.QueryRow(ctx, q, args...))
	if err != nil {
		return nil, fmt.Errorf("update settings: %w", err)
	}
	return row, nil
}

// LookupUserByEmail / ByUsername power the privacy-preserving link-request
// flow. They run in both branches (hit and miss) so timing leaks don't
// reveal whether a user exists.
func (s *Store) LookupUserByEmail(ctx context.Context, email string) (*uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT id FROM users WHERE LOWER(email) = LOWER($1) AND status = 'active'`,
		strings.TrimSpace(email)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup email: %w", err)
	}
	return &id, nil
}

func (s *Store) LookupUserByUsername(ctx context.Context, username string) (*uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT id FROM users WHERE LOWER(username) = LOWER($1) AND status = 'active'`,
		strings.TrimSpace(username)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup username: %w", err)
	}
	return &id, nil
}
