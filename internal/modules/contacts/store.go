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
)

// linked_user_icon_code is projected via scalar subquery so it works in both
// SELECT and INSERT/UPDATE ... RETURNING. NULL when the contact isn't linked
// or the linked user has no icon set. Spec §4.4: client picks
// linked_user_icon_code over the contact's own icon_code when present.
const contactColumns = `id, user_id, display_name, email, phone, notes, icon_code,
	linked_user_id, status, last_used_at, created_at, updated_at,
	(SELECT icon_code FROM users WHERE id = contacts.linked_user_id) AS linked_user_icon_code`

func scanContact(row pgx.Row) (*Contact, error) {
	var c Contact
	var iconBytes, linkedIconBytes []byte
	err := row.Scan(
		&c.ID, &c.UserID, &c.DisplayName, &c.Email, &c.Phone, &c.Notes, &iconBytes,
		&c.LinkedUserID, &c.Status, &c.LastUsedAt, &c.CreatedAt, &c.UpdatedAt,
		&linkedIconBytes,
	)
	if err != nil {
		return nil, err
	}
	if iconBytes != nil {
		c.IconCode = new(shared.IconCode)
		if err := json.Unmarshal(iconBytes, c.IconCode); err != nil {
			return nil, fmt.Errorf("unmarshal icon_code: %w", err)
		}
	}
	if linkedIconBytes != nil {
		c.LinkedUserIconCode = new(shared.IconCode)
		if err := json.Unmarshal(linkedIconBytes, c.LinkedUserIconCode); err != nil {
			return nil, fmt.Errorf("unmarshal linked_user_icon_code: %w", err)
		}
	}
	return &c, nil
}

func (s *Store) Create(ctx context.Context, userID uuid.UUID, req CreateContactRequest) (*Contact, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	var iconJSON []byte
	if req.IconCode != nil {
		var err error
		if iconJSON, err = json.Marshal(req.IconCode); err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
	}

	q := `INSERT INTO contacts
		(id, user_id, display_name, email, phone, notes, icon_code, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $2, $2)
		RETURNING ` + contactColumns

	c, err := scanContact(s.db.QueryRow(ctx, q,
		id, userID, strings.TrimSpace(req.DisplayName),
		req.Email, req.Phone, req.Notes, iconJSON,
	))
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return c, nil
}

func (s *Store) GetByID(ctx context.Context, userID, id uuid.UUID) (*Contact, error) {
	q := `SELECT ` + contactColumns + `
		FROM contacts WHERE id = $1 AND user_id = $2`
	c, err := scanContact(s.db.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrContactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return c, nil
}

// ListFilter mirrors the §3.2 query params. status="" means active-only by
// spec default; status="all" matches both. linked is *bool to distinguish
// "filter unset" from "filter linked=false".
type ListFilter struct {
	Status string
	Linked *bool
	Search string
}

func (s *Store) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]Contact, error) {
	q := `SELECT ` + contactColumns + ` FROM contacts WHERE user_id = $1`
	args := []any{userID}

	switch strings.ToLower(strings.TrimSpace(f.Status)) {
	case "", "active":
		q += ` AND status = 'active'`
	case "archived":
		q += ` AND status = 'archived'`
	case "all":
		// no filter
	default:
		// Treat unknown values as default (active) rather than 500-ing.
		q += ` AND status = 'active'`
	}

	if f.Linked != nil {
		if *f.Linked {
			q += ` AND linked_user_id IS NOT NULL`
		} else {
			q += ` AND linked_user_id IS NULL`
		}
	}

	if s := strings.TrimSpace(f.Search); s != "" {
		args = append(args, "%"+strings.ToLower(s)+"%")
		q += fmt.Sprintf(` AND LOWER(display_name) LIKE $%d`, len(args))
	}

	// Recent-first ranking: contacts the user just split with float to the
	// top of the typeahead. Alphabetical falls in for ties / never-used.
	q += ` ORDER BY last_used_at DESC NULLS LAST,
		LOWER(display_name)`

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

// Update applies a partial patch. Only non-nil fields in req are written.
// Returns the refreshed row.
func (s *Store) Update(ctx context.Context, userID, id uuid.UUID, req UpdateContactRequest) (*Contact, error) {
	q := `UPDATE contacts SET updated_by_user_id = $1`
	args := []any{userID}

	if req.DisplayName != nil {
		args = append(args, strings.TrimSpace(*req.DisplayName))
		q += fmt.Sprintf(", display_name = $%d", len(args))
	}
	if req.Email != nil {
		args = append(args, *req.Email)
		q += fmt.Sprintf(", email = $%d", len(args))
	}
	if req.Phone != nil {
		args = append(args, *req.Phone)
		q += fmt.Sprintf(", phone = $%d", len(args))
	}
	if req.Notes != nil {
		args = append(args, *req.Notes)
		q += fmt.Sprintf(", notes = $%d", len(args))
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
	q += fmt.Sprintf(` WHERE id = $%d AND user_id = $1 RETURNING `, len(args)) + contactColumns

	c, err := scanContact(s.db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrContactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return c, nil
}

// SetStatus flips active <-> archived. Returns ErrAlreadyArchived /
// ErrNotArchived if the requested transition is a no-op so the handler
// can map to a 400.
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

	q := `UPDATE contacts
		SET status = $1, updated_by_user_id = $2
		WHERE id = $3 AND user_id = $2
		RETURNING ` + contactColumns
	c, err := scanContact(s.db.QueryRow(ctx, q, target, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrContactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return c, nil
}

// SetLinkedUserIDTx sets contacts.linked_user_id inside the caller's tx.
// Used by the link-request accept flow.
func (s *Store) SetLinkedUserIDTx(
	ctx context.Context, tx pgx.Tx, ownerUserID, contactID, linkedUserID uuid.UUID,
) (*Contact, error) {
	q := `UPDATE contacts SET linked_user_id = $1, updated_by_user_id = $2
		WHERE id = $3 AND user_id = $2
		RETURNING ` + contactColumns
	c, err := scanContact(tx.QueryRow(ctx, q, linkedUserID, ownerUserID, contactID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrContactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("set linked_user_id: %w", err)
	}
	return c, nil
}

// ClearLinkedUserID is the unlink path. Returns ErrNotLinked if the contact
// wasn't linked to begin with.
func (s *Store) ClearLinkedUserID(ctx context.Context, userID, contactID uuid.UUID) (*Contact, error) {
	current, err := s.GetByID(ctx, userID, contactID)
	if err != nil {
		return nil, err
	}
	if current.LinkedUserID == nil {
		return nil, ErrNotLinked
	}
	q := `UPDATE contacts SET linked_user_id = NULL, updated_by_user_id = $1
		WHERE id = $2 AND user_id = $1
		RETURNING ` + contactColumns
	return scanContact(s.db.QueryRow(ctx, q, userID, contactID))
}

// LinkExistsBetween returns true if EITHER:
//   - userA already has any contact with linked_user_id = userB, OR
//   - userB already has any contact with linked_user_id = userA.
// Used by link-request accept to enforce uniqueness in both directions.
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

// GetByIDTx fetches under tx (no row lock). Used by link-request accept
// after the notification row is locked.
func (s *Store) GetByIDTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Contact, error) {
	q := `SELECT ` + contactColumns + `
		FROM contacts WHERE id = $1 AND user_id = $2`
	c, err := scanContact(tx.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrContactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return c, nil
}

// Delete hard-deletes the contact. In 1b.1 there are no shared_expense_splits
// rows referencing it, so no person_name restoration is needed yet. 1b.1.b
// will inject a SplitsRestorer that runs the UPDATE before this DELETE.
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
