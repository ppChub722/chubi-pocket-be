package categories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
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
	icon_code, sort_order, include_in_report, description, note,
	status, created_at, updated_at`

func scanCategory(row pgx.Row) (*Category, error) {
	var c Category
	var iconBytes []byte
	err := row.Scan(
		&c.ID, &c.UserID, &c.Name, &c.Type, &c.ParentID, &c.IsSystem, &c.SystemKind,
		&iconBytes, &c.SortOrder, &c.IncludeInReport, &c.Description, &c.Note,
		&c.Status, &c.CreatedAt, &c.UpdatedAt,
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
	return &c, nil
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
	// Stable client-side ordering: type → parent group → user's manual sort
	// (sort_order ASC) → fall back to creation time when sort_order ties.
	// Clients reconstruct the tree from this flat list and trust the order.
	q += " ORDER BY type, COALESCE(parent_id::text, ''), sort_order, created_at"

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

	// Resolve next sort_order. Newly-created categories slot in at the
	// FRONT of their sibling group (sort_order = MIN-1) so the user sees
	// what they just made without scrolling. Negative values are fine —
	// sort_order is just an ordering key, never displayed.
	//
	// Done as a separate query rather than inlined as a subquery in the
	// INSERT VALUES list: inlining caused Postgres prepared-statement
	// parameter inference to fail (SQLSTATE 42P08) because the same
	// positional param appeared in both an INSERT column position and a
	// subquery comparison.
	var nextSort int
	err = s.db.QueryRow(ctx, `
		SELECT COALESCE(MIN(sort_order), 0) - 1
		  FROM categories
		 WHERE user_id = $1 AND type = $2
		   AND parent_id IS NOT DISTINCT FROM $3`,
		c.UserID, c.Type, c.ParentID,
	).Scan(&nextSort)
	if err != nil {
		return nil, fmt.Errorf("compute sort_order: %w", err)
	}

	var iconJSON []byte
	if c.IconCode != nil {
		if iconJSON, err = json.Marshal(c.IconCode); err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
	}

	q := `INSERT INTO categories
		(id, user_id, name, type, parent_id, is_system, system_kind, icon_code,
		 sort_order, include_in_report, description, note,
		 status, created_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12,
			'active', $2)
		RETURNING ` + categoryColumns
	created, err := scanCategory(s.db.QueryRow(ctx, q,
		c.ID, c.UserID, c.Name, c.Type, c.ParentID, c.IsSystem, c.SystemKind,
		iconJSON, nextSort, c.IncludeInReport, c.Description, c.Note))
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
// UpdateFields carries the partial-update payload for a single row. Pointer
// fields encode "leave alone" (nil) vs "set to value"; the *Change bools
// distinguish "leave alone" from "explicitly clear" for nullable columns.
type UpdateFields struct {
	Name              *string
	ParentID          *uuid.UUID
	ParentIDChange    bool
	IconCode          *shared.IconCode
	IconCodeChange    bool
	IncludeInReport   *bool
	Description       *string
	DescriptionChange bool
	Note              *string
	NoteChange        bool
}

func (s *Store) Update(
	ctx context.Context, userID, id uuid.UUID,
	f UpdateFields,
) (*Category, error) {
	q := `UPDATE categories SET updated_by_user_id = $1`
	args := []any{userID}
	if f.Name != nil {
		args = append(args, *f.Name)
		q += fmt.Sprintf(", name = $%d", len(args))
	}
	if f.ParentIDChange {
		args = append(args, f.ParentID)
		q += fmt.Sprintf(", parent_id = $%d", len(args))
	}
	if f.IconCodeChange {
		var iconJSON []byte
		if f.IconCode != nil {
			var err error
			if iconJSON, err = json.Marshal(f.IconCode); err != nil {
				return nil, fmt.Errorf("marshal icon_code: %w", err)
			}
		}
		args = append(args, iconJSON)
		q += fmt.Sprintf(", icon_code = $%d::jsonb", len(args))
	}
	if f.IncludeInReport != nil {
		args = append(args, *f.IncludeInReport)
		q += fmt.Sprintf(", include_in_report = $%d", len(args))
	}
	if f.DescriptionChange {
		args = append(args, f.Description)
		q += fmt.Sprintf(", description = $%d", len(args))
	}
	if f.NoteChange {
		args = append(args, f.Note)
		q += fmt.Sprintf(", note = $%d", len(args))
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

// --- Reorder (PATCH /v1/categories/reorder) ---

// ReorderTx applies a batch of (parent_id, sort_order) updates atomically.
// All entries belong to one user; the caller (Service) is responsible for
// validating ownership, depth, cycles, and same-type parents BEFORE calling
// this. Store-side this is just the write — wrap in a tx so a partial apply
// never leaks.
//
// Returns the user's full active category list (post-reorder) so the client
// can replace its cache without a follow-up GET.
func (s *Store) ReorderTx(ctx context.Context, userID uuid.UUID, entries []ReorderEntry) ([]Category, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, e := range entries {
		tag, err := tx.Exec(ctx, `
			UPDATE categories
			SET parent_id = $1, sort_order = $2, updated_by_user_id = $3
			WHERE id = $4 AND user_id = $3 AND status = 'active'`,
			e.ParentID, e.SortOrder, userID, e.ID)
		if err != nil {
			return nil, fmt.Errorf("reorder %s: %w", e.ID, err)
		}
		if tag.RowsAffected() == 0 {
			// Either the row doesn't exist, isn't owned by user, or is archived.
			// All of these are spec-§3.13 errors; Service layer already prevalidated
			// so reaching here means the tree shifted underneath us.
			return nil, ErrCategoryNotFound
		}
	}

	// Re-read inside the same tx so the response reflects the post-write state.
	rows, err := tx.Query(ctx, `SELECT `+categoryColumns+`
		FROM categories
		WHERE user_id = $1 AND status = 'active'
		ORDER BY type, COALESCE(parent_id::text, ''), sort_order, created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("list after reorder: %w", err)
	}
	out := make([]Category, 0)
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, *c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reorder: %w", err)
	}
	return out, nil
}

// --- Seed (called inside auth.Service.Register tx) ---

// SeedForUser inserts the 6 system + starter user categories for a freshly
// registered user. Caller passes a pgx.Tx so it composes with the user insert.
//
// `sort_order` is sequential within each (type, parent_id) sibling group:
// system rows first (0..N), then starter roots, then per-parent children
// from 0. `Store.Create` later inserts new user-created rows at MIN-1 of
// the visible group, surfacing them at the top.
//
// Icon / color / description / include_in_report mirror the Flutter mock
// seed 1:1 — see [seed.go] for the shared catalog data.
func (s *Store) SeedForUserTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	const insertCategory = `
		INSERT INTO categories
			(id, user_id, name, type, parent_id, is_system, system_kind,
			 icon_code, sort_order, include_in_report, description,
			 status, created_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11,
			'active', $12)`

	// 1. System cats — parent_id = NULL, no description. created_by_user_id
	// is NULL because system rows are app-owned, not user-owned.
	systemSortByType := map[string]int{"income": 0, "expense": 0}
	for _, sc := range systemCatalog {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid: %w", err)
		}
		kind := string(sc.Kind)
		iconJSON, err := json.Marshal(seedIconCode(sc.Icon, sc.Color))
		if err != nil {
			return fmt.Errorf("marshal icon_code: %w", err)
		}
		_, err = tx.Exec(ctx, insertCategory,
			id, userID, sc.Name, sc.Type, nil, true, kind,
			iconJSON, systemSortByType[sc.Type], sc.IncludeInReport, nil,
			nil, // created_by_user_id = NULL (system row)
		)
		if err != nil {
			return fmt.Errorf("seed system %s: %w", sc.Kind, err)
		}
		systemSortByType[sc.Type]++
	}

	// 2. Starter expense roots + their children. Root sort_order continues
	// from where system rows left off so system rows surface above starters
	// when both are visible (e.g. transaction picker with include_system=true).
	rootSortExpense := systemSortByType["expense"]
	for _, root := range starterCatalog {
		rootID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid: %w", err)
		}
		rootIconJSON, err := json.Marshal(seedIconCode(root.Icon, root.Color))
		if err != nil {
			return fmt.Errorf("marshal icon_code: %w", err)
		}
		_, err = tx.Exec(ctx, insertCategory,
			rootID, userID, root.Name, "expense", nil, false, nil,
			rootIconJSON, rootSortExpense, true, nil,
			userID,
		)
		if err != nil {
			return fmt.Errorf("seed root %s: %w", root.Name, err)
		}
		rootSortExpense++

		for ci, child := range root.Children {
			childID, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("uuid: %w", err)
			}
			// Children inherit root's background color; only icon differs.
			childIconJSON, err := json.Marshal(seedIconCode(child.Icon, root.Color))
			if err != nil {
				return fmt.Errorf("marshal icon_code: %w", err)
			}
			_, err = tx.Exec(ctx, insertCategory,
				childID, userID, child.Name, "expense", rootID, false, nil,
				childIconJSON, ci, child.IncludeInReport, child.Description,
				userID,
			)
			if err != nil {
				return fmt.Errorf("seed child %s/%s: %w", root.Name, child.Name, err)
			}
		}
	}

	// 3. Starter income — flat (no parents). Sort order continues from
	// system income rows.
	incomeSort := systemSortByType["income"]
	for _, in := range starterIncomeCatalog {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid: %w", err)
		}
		iconJSON, err := json.Marshal(seedIconCode(in.Icon, in.Color))
		if err != nil {
			return fmt.Errorf("marshal icon_code: %w", err)
		}
		_, err = tx.Exec(ctx, insertCategory,
			id, userID, in.Name, "income", nil, false, nil,
			iconJSON, incomeSort, in.IncludeInReport, in.Description,
			userID,
		)
		if err != nil {
			return fmt.Errorf("seed income %s: %w", in.Name, err)
		}
		incomeSort++
	}

	return nil
}

// seedIconCode converts old-style Icon (art ID) + Color (hex bg) seed fields
// into an IconCode JSONB value. Color is the circle background; icon is always
// white on the colored background, matching the legacy app rendering.
func seedIconCode(icon, color string) shared.IconCode {
	white := "#FFFFFF"
	ic := shared.IconCode{
		IconColors:   []string{white},
		BgColors:     []string{},
		BorderColors: []string{},
	}
	if icon != "" {
		ic.Icon = &icon
	}
	if color != "" {
		solid := "solid"
		ic.Background = &solid
		ic.BgColors = []string{color}
	}
	return ic
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
