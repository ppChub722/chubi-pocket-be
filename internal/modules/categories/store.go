package categories

import (
	"context"
	"errors"
	"fmt"

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
	ErrCategoryNotFound = errors.New("category not found")
	ErrDuplicateName    = errors.New("category name already exists at this level")
	ErrInvalidParent    = errors.New("parent category is invalid (wrong owner / type / archived / system)")
	ErrCycleDetected    = errors.New("parent change would create a cycle")
	ErrMaxDepth         = errors.New("category tree depth limit exceeded (max 3)")
	ErrSystemImmutable  = errors.New("system category cannot be modified this way")
	ErrNotArchived      = errors.New("category is not archived")
	ErrHasTransactions  = errors.New("category still has transactions and cannot be permanently deleted")
)

const categoryColumns = `id, user_id, name, type, parent_id, is_system, system_kind,
	icon, color, status, created_at, updated_at`

func scanCategory(row pgx.Row) (*Category, error) {
	var c Category
	err := row.Scan(
		&c.ID, &c.UserID, &c.Name, &c.Type, &c.ParentID, &c.IsSystem, &c.SystemKind,
		&c.Icon, &c.Color, &c.Status, &c.CreatedAt, &c.UpdatedAt,
	)
	return &c, err
}

// --- Read ---

func (s *Store) GetByID(ctx context.Context, userID, id uuid.UUID) (*Category, error) {
	q := `SELECT ` + categoryColumns + ` FROM categories WHERE id = $1 AND user_id = $2`
	c, err := scanCategory(s.db.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCategoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return c, nil
}

// ListFilter mirrors the GET /v1/categories query params.
type ListFilter struct {
	Type          *string // "income" | "expense" | nil
	Status        string  // "active" | "archived" | "all" — defaults to "active" at handler
	IncludeSystem bool
}

func (s *Store) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]Category, error) {
	q := `SELECT ` + categoryColumns + ` FROM categories WHERE user_id = $1`
	args := []any{userID}

	if f.Type != nil {
		args = append(args, *f.Type)
		q += fmt.Sprintf(" AND type = $%d", len(args))
	}
	if f.Status != "all" {
		args = append(args, f.Status)
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if !f.IncludeSystem {
		q += " AND is_system = FALSE"
	}
	q += " ORDER BY type, COALESCE(parent_id::text, ''), LOWER(name)"

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	defer rows.Close()

	out := make([]Category, 0)
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// CountChildren returns the number of active categories whose parent is `id`.
func (s *Store) CountChildren(ctx context.Context, id uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM categories WHERE parent_id = $1 AND status = 'active'`,
		id).Scan(&n)
	return n, err
}

// Depth returns 1 for a root, 2 for a child of a root, 3 for a grandchild.
// Walks `parent_id` chain; safe (assumes no cycle, which the cycle check enforces).
func (s *Store) DepthFromParent(ctx context.Context, parentID *uuid.UUID) (int, error) {
	if parentID == nil {
		return 1, nil
	}
	depth := 1
	current := parentID
	for current != nil {
		depth++
		if depth > 4 { // guard rail; should never trigger
			return depth, ErrMaxDepth
		}
		var next *uuid.UUID
		err := s.db.QueryRow(ctx,
			`SELECT parent_id FROM categories WHERE id = $1`, *current).Scan(&next)
		if errors.Is(err, pgx.ErrNoRows) {
			return depth, ErrInvalidParent
		}
		if err != nil {
			return depth, fmt.Errorf("walk parent: %w", err)
		}
		current = next
	}
	return depth, nil
}

// IsDescendant returns true if `candidate` is `ancestor` or anywhere in
// `ancestor`'s subtree. Used for cycle detection on PUT (parent change).
func (s *Store) IsDescendant(ctx context.Context, ancestor, candidate uuid.UUID) (bool, error) {
	if ancestor == candidate {
		return true, nil
	}
	// Walk children breadth-first. With a 3-deep tree the subtree is tiny.
	frontier := []uuid.UUID{ancestor}
	for len(frontier) > 0 {
		rows, err := s.db.Query(ctx,
			`SELECT id FROM categories WHERE parent_id = ANY($1)`, frontier)
		if err != nil {
			return false, fmt.Errorf("walk subtree: %w", err)
		}
		next := []uuid.UUID{}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return false, err
			}
			if id == candidate {
				rows.Close()
				return true, nil
			}
			next = append(next, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return false, err
		}
		frontier = next
	}
	return false, nil
}

// LookupSystem returns the category ID for (userID, kind) — used by other
// modules' auto-assignment paths (transfers, opening balance, adjust balance).
func (s *Store) LookupSystem(ctx context.Context, userID uuid.UUID, kind SystemKind) (*Category, error) {
	q := `SELECT ` + categoryColumns + `
		FROM categories
		WHERE user_id = $1 AND is_system = TRUE AND system_kind = $2`
	c, err := scanCategory(s.db.QueryRow(ctx, q, userID, string(kind)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCategoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup system: %w", err)
	}
	return c, nil
}

// --- Write ---

func (s *Store) Create(ctx context.Context, c *Category) (*Category, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	c.ID = id

	q := `INSERT INTO categories
		(id, user_id, name, type, parent_id, is_system, system_kind, icon, color, status, created_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'active', $2)
		RETURNING ` + categoryColumns
	created, err := scanCategory(s.db.QueryRow(ctx, q,
		c.ID, c.UserID, c.Name, c.Type, c.ParentID, c.IsSystem, c.SystemKind,
		c.Icon, c.Color))
	if err != nil {
		return nil, mapInsertError(err)
	}
	return created, nil
}

// Update applies a partial update. Caller (service layer) does cycle / depth
// / duplicate checks.
//
// `parentChange = false` means leave parent_id alone; when true, `parentID`
// is the new value (nil → set NULL / make root).
//
// For icon / color: nil = leave unchanged; non-nil = set to that string
// (empty string clears the column). Spec §3.4 allows partial updates; this
// pragmatic 1a convention avoids adding more presence flags for fields that
// are rarely cleared.
func (s *Store) Update(
	ctx context.Context, userID, id uuid.UUID,
	name *string, parentID *uuid.UUID, parentChange bool,
	icon, color *string,
) (*Category, error) {
	q := `UPDATE categories SET updated_by_user_id = $1`
	args := []any{userID}
	if name != nil {
		args = append(args, *name)
		q += fmt.Sprintf(", name = $%d", len(args))
	}
	if parentChange {
		args = append(args, parentID)
		q += fmt.Sprintf(", parent_id = $%d", len(args))
	}
	if icon != nil {
		args = append(args, *icon)
		q += fmt.Sprintf(", icon = $%d", len(args))
	}
	if color != nil {
		args = append(args, *color)
		q += fmt.Sprintf(", color = $%d", len(args))
	}
	args = append(args, id)
	q += fmt.Sprintf(" WHERE id = $%d AND user_id = $1 RETURNING ", len(args)) + categoryColumns

	c, err := scanCategory(s.db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCategoryNotFound
	}
	if err != nil {
		return nil, mapInsertError(err)
	}
	return c, nil
}

func (s *Store) SetStatus(ctx context.Context, userID, id uuid.UUID, status string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE categories SET status = $1, updated_by_user_id = $2
		 WHERE id = $3 AND user_id = $2`,
		status, userID, id)
	if err != nil {
		return fmt.Errorf("db error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCategoryNotFound
	}
	return nil
}

// HardDelete removes the row. Caller (service) must reparent children first.
func (s *Store) HardDelete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM categories WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("db error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCategoryNotFound
	}
	return nil
}

// ReparentChildren shifts all direct children of `id` to `id`'s parent
// (grandparent-shift). Used on hard delete to avoid orphans.
func (s *Store) ReparentChildren(ctx context.Context, userID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE categories
		SET parent_id = (SELECT parent_id FROM categories WHERE id = $1 AND user_id = $2),
		    updated_by_user_id = $2
		WHERE parent_id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("reparent children: %w", err)
	}
	return nil
}

// FindNearestActiveAncestor walks up `from`'s parent chain and returns the
// first ancestor whose status is 'active'. Returns (nil, nil) when no active
// ancestor exists (caller should make `from` a root). Used by Restore when
// the original parent is still archived.
func (s *Store) FindNearestActiveAncestor(ctx context.Context, from uuid.UUID) (*uuid.UUID, error) {
	var parentID *uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT parent_id FROM categories WHERE id = $1`, from).Scan(&parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read self parent: %w", err)
	}

	for parentID != nil {
		var (
			nextParent *uuid.UUID
			status     string
		)
		err := s.db.QueryRow(ctx,
			`SELECT parent_id, status FROM categories WHERE id = $1`, *parentID,
		).Scan(&nextParent, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("walk ancestors: %w", err)
		}
		if status == "active" {
			return parentID, nil
		}
		parentID = nextParent
	}
	return nil, nil
}

// --- Seed (called inside auth.Service.Register tx) ---

// SeedForUser inserts the 6 system + starter user categories for a freshly
// registered user. Caller passes a pgx.Tx so it composes with the user insert.
func (s *Store) SeedForUserTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	// 1. System cats
	for _, sc := range systemCatalog {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO categories
				(id, user_id, name, type, parent_id, is_system, system_kind, status, created_by_user_id)
			VALUES ($1, $2, $3, $4, NULL, TRUE, $5, 'active', NULL)`,
			id, userID, sc.Name, sc.Type, string(sc.Kind))
		if err != nil {
			return fmt.Errorf("seed system %s: %w", sc.Kind, err)
		}
	}

	// 2. Starter cats — parents first, then their children pointing at them.
	for _, root := range starterCatalog {
		rootID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO categories
				(id, user_id, name, type, parent_id, is_system, system_kind, status, created_by_user_id)
			VALUES ($1, $2, $3, $4, NULL, FALSE, NULL, 'active', $2)`,
			rootID, userID, root.Name, root.Type)
		if err != nil {
			return fmt.Errorf("seed root %s: %w", root.Name, err)
		}
		for _, childName := range root.Children {
			childID, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("uuid: %w", err)
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO categories
					(id, user_id, name, type, parent_id, is_system, system_kind, status, created_by_user_id)
				VALUES ($1, $2, $3, $4, $5, FALSE, NULL, 'active', $2)`,
				childID, userID, childName, root.Type, rootID)
			if err != nil {
				return fmt.Errorf("seed child %s/%s: %w", root.Name, childName, err)
			}
		}
	}
	return nil
}

// --- Helpers ---

func mapInsertError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// Unique violation. The two relevant indexes are:
		//   idx_categories_name        — sibling-name uniqueness
		//   idx_categories_system_kind — system kind uniqueness
		return ErrDuplicateName
	}
	return fmt.Errorf("db error: %w", err)
}
