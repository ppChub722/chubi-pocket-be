package contacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

func (s *Store) Pool() *pgxpool.Pool { return s.db }

var (
	ErrContactNotFound = errors.New("contact not found")
	ErrNotArchived     = errors.New("contact is not archived")
	ErrAlreadyArchived = errors.New("contact is already archived")
	ErrAlreadyLinked   = errors.New("contact is already linked")
	ErrNotLinked       = errors.New("contact is not linked")
	ErrCannotLinkSelf  = errors.New("cannot link a contact to yourself")
	ErrNotLinkRequest  = errors.New("notification is not a contact_link_request")
	ErrRequestRejected = errors.New("link request was rejected")
	ErrNotAccepted     = errors.New("link request has not been accepted")
)

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var contactSelect = `SELECT c.id, c.user_id,
	COALESCE(` + shared.PersonNameSQL("c", "lu") + `, c.display_name),
	c.nickname, c.show_nickname, c.display_name,
	CASE WHEN lu.id IS NOT NULL THEN lu.email ELSE c.email END,
	c.phone, c.description, c.note, ` + shared.PersonIconSQL("c", "lu") + `,
	c.linked_user_id, c.status, c.last_used_at, c.created_at, c.updated_at,
	lu.id, lu.display_name, lu.email, lu.icon_code, lu.username
	FROM contacts c LEFT JOIN users lu ON lu.id = c.linked_user_id`

func scanContact(row pgx.Row) (*Contact, error) {
	var (
		c         Contact
		iconBytes []byte
		luID      *uuid.UUID
		luName    *string
		luEmail   *string
		luIcon    []byte
		luUser    *string
	)
	err := row.Scan(
		&c.ID, &c.UserID, &c.Name, &c.Nickname, &c.ShowNickname, &c.DisplayName,
		&c.Email, &c.Phone, &c.Description, &c.Note, &iconBytes,
		&c.LinkedUserID, &c.Status, &c.LastUsedAt, &c.CreatedAt, &c.UpdatedAt,
		&luID, &luName, &luEmail, &luIcon, &luUser,
	)
	if err != nil {
		return nil, err
	}
	c.IconCode = shared.IconFromJSON(iconBytes)
	if luID != nil {
		lu := &LinkedUser{ID: *luID, Email: luEmail, IconCode: shared.IconFromJSON(luIcon)}
		if luName != nil {
			lu.DisplayName = *luName
		}
		if luUser != nil {
			lu.Username = *luUser
		}
		c.LinkedUser = lu
	}
	return &c, nil
}

func (s *Store) get(ctx context.Context, q rowQuerier, userID, id uuid.UUID) (*Contact, error) {
	c, err := scanContact(q.QueryRow(ctx, contactSelect+` WHERE c.id = $1 AND c.user_id = $2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrContactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return c, nil
}

func iconJSON(ic *shared.IconCode) ([]byte, error) {
	if ic == nil {
		return nil, nil
	}
	b, err := json.Marshal(ic)
	if err != nil {
		return nil, fmt.Errorf("marshal icon_code: %w", err)
	}
	return b, nil
}

func (s *Store) Create(ctx context.Context, userID uuid.UUID, req CreateContactRequest) (*Contact, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	icon, err := iconJSON(req.IconCode)
	if err != nil {
		return nil, err
	}
	show := true
	if req.ShowNickname != nil {
		show = *req.ShowNickname
	}
	_, err = s.db.Exec(ctx, `INSERT INTO contacts
		(id, user_id, display_name, nickname, show_nickname, email, phone, description, note, icon_code,
		 created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $2, $2)`,
		id, userID, strings.TrimSpace(req.DisplayName), shared.CleanText(req.Nickname), show,
		req.Email, req.Phone, shared.CleanText(req.Description), shared.CleanText(req.Note), icon)
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return s.get(ctx, s.db, userID, id)
}

func (s *Store) CreateLinkedTx(
	ctx context.Context, tx pgx.Tx,
	userID, linkedUserID uuid.UUID,
	displayName string,
	email, phone, description, note *string,
	iconCode *shared.IconCode,
) (*Contact, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	icon, err := iconJSON(iconCode)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO contacts
		(id, user_id, display_name, email, phone, description, note, icon_code, linked_user_id,
		 created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $2, $2)`,
		id, userID, strings.TrimSpace(displayName),
		email, phone, shared.CleanText(description), shared.CleanText(note), icon, linkedUserID,
	); err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return s.get(ctx, tx, userID, id)
}

func (s *Store) GetByID(ctx context.Context, userID, id uuid.UUID) (*Contact, error) {
	return s.get(ctx, s.db, userID, id)
}

// GetByIDTx fetches under tx (no row lock).
func (s *Store) GetByIDTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Contact, error) {
	return s.get(ctx, tx, userID, id)
}

type ListFilter struct {
	Status string
	Linked *bool
	Search string
}

func (s *Store) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]Contact, error) {
	resolved := `COALESCE(` + shared.PersonNameSQL("c", "lu") + `, c.display_name)`
	q := contactSelect + ` WHERE c.user_id = $1`
	args := []any{userID}

	switch strings.ToLower(strings.TrimSpace(f.Status)) {
	case "", "active":
		q += ` AND c.status = 'active'`
	case "archived":
		q += ` AND c.status = 'archived'`
	case "all":
	default:
		q += ` AND c.status = 'active'`
	}

	if f.Linked != nil {
		if *f.Linked {
			q += ` AND c.linked_user_id IS NOT NULL`
		} else {
			q += ` AND c.linked_user_id IS NULL`
		}
	}

	if s := strings.TrimSpace(f.Search); s != "" {
		args = append(args, "%"+shared.EscapeLike(strings.ToLower(s))+"%")
		n := len(args)
		cond := fmt.Sprintf(`LOWER(%[2]s) LIKE $%[1]d
			OR LOWER(COALESCE(c.nickname, '')) LIKE $%[1]d
			OR LOWER(c.display_name) LIKE $%[1]d
			OR LOWER(COALESCE(lu.display_name, '')) LIKE $%[1]d
			OR LOWER(COALESCE(c.email, '')) LIKE $%[1]d
			OR LOWER(COALESCE(lu.email, '')) LIKE $%[1]d`, n, resolved)
		if digits := onlyDigits(s); digits != "" {
			args = append(args, "%"+digits+"%")
			cond += fmt.Sprintf(` OR regexp_replace(COALESCE(c.phone, ''), '\D', '', 'g') LIKE $%d`, len(args))
		}
		q += ` AND (` + cond + `)`
	}

	q += ` ORDER BY (c.status = 'active') DESC, LOWER(` + resolved + `), c.id`

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	defer rows.Close()

	out := make([]Contact, 0)
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) Update(ctx context.Context, userID, id uuid.UUID, req UpdateContactRequest) (*Contact, error) {
	q := `UPDATE contacts SET updated_by_user_id = $1`
	args := []any{userID}

	if req.DisplayName != nil {
		args = append(args, strings.TrimSpace(*req.DisplayName))
		q += fmt.Sprintf(", display_name = $%d", len(args))
	}
	if v, ok := req.NicknameChange(); ok {
		args = append(args, v)
		q += fmt.Sprintf(", nickname = $%d", len(args))
	}
	if req.ShowNickname != nil {
		args = append(args, *req.ShowNickname)
		q += fmt.Sprintf(", show_nickname = $%d", len(args))
	}
	if req.Email != nil {
		args = append(args, *req.Email)
		q += fmt.Sprintf(", email = $%d", len(args))
	}
	if req.Phone != nil {
		args = append(args, *req.Phone)
		q += fmt.Sprintf(", phone = $%d", len(args))
	}
	if v, ok := req.DescriptionChange(); ok {
		args = append(args, v)
		q += fmt.Sprintf(", description = $%d", len(args))
	}
	if v, ok := req.NoteChange(); ok {
		args = append(args, v)
		q += fmt.Sprintf(", note = $%d", len(args))
	}
	if req.IconCode != nil {
		b, err := json.Marshal(req.IconCode)
		if err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
		args = append(args, b)
		q += fmt.Sprintf(", icon_code = $%d::jsonb", len(args))
	}

	args = append(args, id)
	q += fmt.Sprintf(` WHERE id = $%d AND user_id = $1`, len(args))

	tag, err := s.db.Exec(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrContactNotFound
	}
	return s.get(ctx, s.db, userID, id)
}

func (s *Store) SetStatus(ctx context.Context, userID, id uuid.UUID, target string) (*Contact, error) {
	current, err := s.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if target == StatusArchived && current.Status == StatusArchived {
		return nil, ErrAlreadyArchived
	}
	if target == StatusActive && current.Status != StatusArchived {
		return nil, ErrNotArchived
	}
	if _, err := s.db.Exec(ctx, `UPDATE contacts SET status = $1, updated_by_user_id = $2
		WHERE id = $3 AND user_id = $2`, target, userID, id); err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return s.get(ctx, s.db, userID, id)
}

func (s *Store) SetLinkedUserIDTx(
	ctx context.Context, tx pgx.Tx, ownerUserID, contactID, linkedUserID uuid.UUID,
) (*Contact, error) {
	tag, err := tx.Exec(ctx, `UPDATE contacts SET linked_user_id = $1, updated_by_user_id = $2
		WHERE id = $3 AND user_id = $2`, linkedUserID, ownerUserID, contactID)
	if err != nil {
		return nil, fmt.Errorf("set linked_user_id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrContactNotFound
	}
	return s.get(ctx, tx, ownerUserID, contactID)
}

func (s *Store) ClearLinkedUserID(ctx context.Context, userID, contactID uuid.UUID) (*Contact, error) {
	current, err := s.GetByID(ctx, userID, contactID)
	if err != nil {
		return nil, err
	}
	if current.LinkedUserID == nil {
		return nil, ErrNotLinked
	}
	if _, err := s.db.Exec(ctx, `UPDATE contacts SET linked_user_id = NULL, updated_by_user_id = $1
		WHERE id = $2 AND user_id = $1`, userID, contactID); err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return s.get(ctx, s.db, userID, contactID)
}

func (s *Store) ContactLinkedTo(ctx context.Context, userID, linkedUserID uuid.UUID) (*uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT id FROM contacts WHERE user_id = $1 AND linked_user_id = $2
		ORDER BY created_at LIMIT 1`, userID, linkedUserID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return &id, nil
}

func (s *Store) LinkExistsBetween(ctx context.Context, tx pgx.Tx, userA, userB uuid.UUID) (bool, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM contacts
		WHERE (user_id = $1 AND linked_user_id = $2)
		   OR (user_id = $2 AND linked_user_id = $1)`,
		userA, userB).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("link existence: %w", err)
	}
	return n > 0, nil
}

func (s *Store) Delete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM contacts WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("db error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrContactNotFound
	}
	return nil
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// DeletePreview counts what a hard delete of the contact would touch.
func (s *Store) DeletePreview(ctx context.Context, userID uuid.UUID, c *Contact) (*DeletePreview, error) {
	p := &DeletePreview{Linked: c.LinkedUserID != nil}
	// Event members no longer point at contacts (migration 027): count the
	// linked account's memberships in the caller's own events.
	if err := s.db.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM personal_debts WHERE user_id = $1 AND counterparty_contact_id = $2),
		(SELECT COUNT(*) FROM project_members pm JOIN projects p ON p.id = pm.project_id
		 WHERE p.owner_user_id = $1 AND pm.user_id = $3 AND pm.left_at IS NULL)`,
		userID, c.ID, c.LinkedUserID).
		Scan(&p.DebtRecords, &p.EventMembers); err != nil {
		return nil, fmt.Errorf("delete preview: %w", err)
	}
	return p, nil
}
