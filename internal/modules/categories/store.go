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
)

const categoryColumns = `id, user_id, name, type, parent_id, is_system, system_kind,
	icon_code, sort_order, include_in_report, description, note,
	created_at, updated_at`

func scanCategory(row pgx.Row) (*Category, error) {
	c := Category{Depth: 1, Path: []PathItem{}}
	var iconBytes []byte
	err := row.Scan(
		&c.ID, &c.UserID, &c.Name, &c.Type, &c.ParentID, &c.IsSystem, &c.SystemKind,
		&iconBytes, &c.SortOrder, &c.IncludeInReport, &c.Description, &c.Note,
		&c.CreatedAt, &c.UpdatedAt,
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

type ListFilter struct {
	Type          *string // "income" | "expense" | nil
	IncludeSystem bool
}

func (s *Store) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]Category, error) {
	q := `SELECT ` + categoryColumns + ` FROM categories WHERE user_id = $1`
	args := []any{userID}

	if f.Type != nil {
		args = append(args, *f.Type)
		q += fmt.Sprintf(" AND type = $%d", len(args))
	}
	if !f.IncludeSystem {
		q += " AND is_system = FALSE"
	}
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

func (s *Store) CountChildren(ctx context.Context, id uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM categories WHERE parent_id = $1`,
		id).Scan(&n)
	return n, err
}

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

func (s *Store) IsDescendant(ctx context.Context, ancestor, candidate uuid.UUID) (bool, error) {
	if ancestor == candidate {
		return true, nil
	}
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

func (s *Store) Create(ctx context.Context, c *Category) (*Category, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	c.ID = id

	var nextSort int
	err = s.db.QueryRow(ctx, `
		SELECT COALESCE(MAX(sort_order), -1) + 1
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
		created_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12,
			$2)
		RETURNING ` + categoryColumns
	created, err := scanCategory(s.db.QueryRow(ctx, q,
		c.ID, c.UserID, c.Name, c.Type, c.ParentID, c.IsSystem, c.SystemKind,
		iconJSON, nextSort, c.IncludeInReport, shared.CleanText(c.Description), shared.CleanText(c.Note)))
	if err != nil {
		return nil, mapInsertError(err)
	}
	return created, nil
}

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
			WHERE id = $4 AND user_id = $3`,
			e.ParentID, e.SortOrder, userID, e.ID)
		if err != nil {
			return nil, fmt.Errorf("reorder %s: %w", e.ID, err)
		}
		if tag.RowsAffected() == 0 {
			return nil, ErrCategoryNotFound
		}
	}

	rows, err := tx.Query(ctx, `SELECT `+categoryColumns+`
		FROM categories
		WHERE user_id = $1
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

func (s *Store) SeedForUserTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	const insertCategory = `
		INSERT INTO categories
			(id, user_id, name, type, parent_id, is_system, system_kind,
			 icon_code, sort_order, include_in_report, description,
			created_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11,
			$12)`

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

func mapInsertError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrDuplicateName
	}
	return fmt.Errorf("db error: %w", err)
}

func (s *Store) CountBudgets(ctx context.Context, id uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(DISTINCT budget_id) FROM budget_categories WHERE category_id = $1`, id).Scan(&n)
	return n, err
}

func (s *Store) IsFeeCategory(ctx context.Context, userID, id uuid.UUID) (bool, error) {
	var is bool
	err := s.db.QueryRow(ctx,
		`SELECT COALESCE(preferences->>'fee_category_id' = $2::text, FALSE) FROM user_preferences WHERE user_id = $1`,
		userID, id.String()).Scan(&is)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return is, err
}

func (s *Store) ChildIDs(ctx context.Context, userID, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id FROM categories WHERE parent_id = $1 AND user_id = $2 ORDER BY sort_order, created_at`, id, userID)
	if err != nil {
		return nil, fmt.Errorf("child ids: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var cid uuid.UUID
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		out = append(out, cid)
	}
	return out, rows.Err()
}

func (s *Store) ByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]Category, error) {
	out := make([]Category, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+categoryColumns+` FROM categories
		WHERE user_id = $1 AND id = ANY($2)`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("by ids: %w", err)
	}
	defer rows.Close()
	byID := map[uuid.UUID]Category{}
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		byID[c.ID] = *c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Store) TreeNames(ctx context.Context, userID uuid.UUID) (map[uuid.UUID]TreeNode, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, parent_id, icon_code FROM categories WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("tree: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]TreeNode{}
	for rows.Next() {
		var (
			id uuid.UUID
			n  TreeNode
		)
		var icon []byte
		if err := rows.Scan(&id, &n.Name, &n.ParentID, &icon); err != nil {
			return nil, err
		}
		n.IconCode = shared.IconFromJSON(icon)
		out[id] = n
	}
	return out, rows.Err()
}

type TreeNode struct {
	Name     string
	ParentID *uuid.UUID
	IconCode *shared.IconCode
}
