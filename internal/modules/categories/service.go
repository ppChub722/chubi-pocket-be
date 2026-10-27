package categories

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxDepth = 3

// TransactionCounter is injected by main.go (wired to transactions.Store.CountByCategory) so that
// DELETE /v1/categories/:id can decide between archive and hard delete.
type TransactionCounter func(ctx context.Context, categoryID uuid.UUID) (int, error)

type Service struct {
	store     *Store
	txCounter TransactionCounter
}

func NewService(s *Store) *Service {
	return &Service{store: s}
}

func (s *Service) WithTransactionCounter(c TransactionCounter) {
	s.txCounter = c
}

type Seeder interface {
	SeedForUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error
}

func (s *Service) SeedForUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	return s.store.SeedForUserTx(ctx, tx, userID)
}

func (s *Service) SystemFor(ctx context.Context, userID uuid.UUID, kind SystemKind) (*Category, error) {
	return s.store.LookupSystem(ctx, userID, kind)
}

func (s *Service) GetCategoryRow(ctx context.Context, userID, id uuid.UUID) (*Category, error) {
	return s.store.GetByID(ctx, userID, id)
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, req CreateCategoryRequest) (*Category, error) {
	if err := s.validateParent(ctx, userID, req.ParentID, req.Type); err != nil {
		return nil, err
	}
	depth, err := s.store.DepthFromParent(ctx, req.ParentID)
	if err != nil {
		return nil, err
	}
	if depth > maxDepth {
		return nil, ErrMaxDepth
	}
	includeInReport := true
	if req.IncludeInReport != nil {
		includeInReport = *req.IncludeInReport
	}
	c := &Category{
		UserID:          userID,
		Name:            strings.TrimSpace(req.Name),
		Type:            req.Type,
		ParentID:        req.ParentID,
		IsSystem:        false,
		IconCode:        req.IconCode,
		IncludeInReport: includeInReport,
		Description:     req.Description,
		Note:            req.Note,
	}
	created, err := s.store.Create(ctx, c)
	if err != nil {
		return nil, err
	}
	return s.decorateOne(ctx, userID, created)
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*CategoryDetail, error) {
	c, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.decorateOne(ctx, userID, c); err != nil {
		return nil, err
	}
	one := []Category{*c}
	if err := s.decorateMonth(ctx, userID, one, ""); err != nil {
		return nil, err
	}
	c.MonthSpent, c.MonthCount = one[0].MonthSpent, one[0].MonthCount
	childCount, err := s.store.CountChildren(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("count children: %w", err)
	}
	txCount, err := s.countTransactions(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("count transactions: %w", err)
	}
	budgetCount, err := s.store.CountBudgets(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("count budgets: %w", err)
	}

	isFee, err := s.store.IsFeeCategory(ctx, userID, c.ID)
	if err != nil {
		return nil, fmt.Errorf("fee category: %w", err)
	}

	out := &CategoryDetail{
		Category:         *c,
		IsFeeCategory:    isFee,
		ChildCount:       childCount,
		TransactionCount: txCount,
		BudgetCount:      budgetCount,
	}
	if c.ParentID != nil {
		parent, err := s.store.GetByID(ctx, userID, *c.ParentID)
		if err == nil {
			out.ParentName = &parent.Name
		}
	}
	return out, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]Category, error) {
	return s.ListMonth(ctx, userID, f, "")
}

func (s *Service) ListMonth(ctx context.Context, userID uuid.UUID, f ListFilter, tz string) ([]Category, error) {
	cats, err := s.store.List(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	cats, err = s.decorate(ctx, userID, cats)
	if err != nil {
		return nil, err
	}
	return cats, s.decorateMonth(ctx, userID, cats, tz)
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateCategoryRequest) (*CategoryDetail, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	// 0.4.1-1 D6: a system category keeps its name (the app words it from system_kind) and stays out
	// of reports; the icon, description and note stay editable. Sending the current value is fine.
	if current.IsSystem {
		if req.Name != nil && strings.TrimSpace(*req.Name) != current.Name {
			return nil, fmt.Errorf("%w: a system category can't be renamed", ErrSystemImmutable)
		}
		if req.IncludeInReport != nil && *req.IncludeInReport != current.IncludeInReport {
			return nil, fmt.Errorf("%w: a system category's include_in_report is fixed", ErrSystemImmutable)
		}
	}

	newParent, parentChanged := req.ParentIDChange()

	if parentChanged {
		// System cats must stay roots — guard explicitly.
		if current.IsSystem && newParent != nil {
			return nil, fmt.Errorf("%w: system categories must remain roots", ErrSystemImmutable)
		}
		if newParent != nil {
			if err := s.validateParent(ctx, userID, newParent, current.Type); err != nil {
				return nil, err
			}
			// Cycle check: new parent must not be `id` or a descendant of `id`.
			cycle, err := s.store.IsDescendant(ctx, id, *newParent)
			if err != nil {
				return nil, err
			}
			if cycle {
				return nil, ErrCycleDetected
			}
			parentDepth, err := s.store.DepthFromParent(ctx, newParent)
			if err != nil {
				return nil, err
			}
			subtree, err := s.subtreeDepth(ctx, id)
			if err != nil {
				return nil, err
			}
			if parentDepth+subtree > maxDepth {
				return nil, ErrMaxDepth
			}
		}
	}

	var name *string
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		name = &trimmed
	}

	desc, descChanged := req.DescriptionChange()
	note, noteChanged := req.NoteChange()

	iconCode, iconCodeChanged := req.IconCodeChange()

	if _, err := s.store.Update(ctx, userID, id, UpdateFields{
		Name:              name,
		ParentID:          newParent,
		ParentIDChange:    parentChanged,
		IconCode:          iconCode,
		IconCodeChange:    iconCodeChanged,
		IncludeInReport:   req.IncludeInReport,
		Description:       desc,
		DescriptionChange: descChanged,
		Note:              note,
		NoteChange:        noteChanged,
	}); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, id)
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) (*DeleteCategoryResult, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if current.IsSystem {
		return nil, fmt.Errorf("%w: system categories cannot be deleted", ErrSystemImmutable)
	}
	childIDs, err := s.store.ChildIDs(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if err := s.store.ReparentChildren(ctx, userID, id); err != nil {
		return nil, err
	}
	if err := s.store.HardDelete(ctx, userID, id); err != nil {
		return nil, err
	}
	moved, err := s.store.ByIDs(ctx, userID, childIDs)
	if err != nil {
		return nil, err
	}
	moved, err = s.decorate(ctx, userID, moved)
	if err != nil {
		return nil, err
	}
	return &DeleteCategoryResult{ID: id, Reparented: moved}, nil
}

func (s *Service) Reorder(ctx context.Context, userID uuid.UUID, entries []ReorderEntry) ([]Category, error) {
	if len(entries) == 0 {
		cats, err := s.store.List(ctx, userID, ListFilter{})
		if err != nil {
			return nil, err
		}
		return s.decorate(ctx, userID, cats)
	}

	all, err := s.store.List(ctx, userID, ListFilter{
		IncludeSystem: true,
	})
	if err != nil {
		return nil, fmt.Errorf("preload: %w", err)
	}
	byID := make(map[uuid.UUID]*Category, len(all))
	for i := range all {
		byID[all[i].ID] = &all[i]
	}

	postParent := make(map[uuid.UUID]*uuid.UUID, len(all))
	for i := range all {
		postParent[all[i].ID] = all[i].ParentID
	}

	for _, e := range entries {
		c, ok := byID[e.ID]
		if !ok {
			return nil, ErrCategoryNotFound
		}
		// System cats: must remain roots (parent_id null).
		if c.IsSystem {
			if e.ParentID != nil {
				return nil, fmt.Errorf("%w: system categories must remain roots", ErrSystemImmutable)
			}
		}
		if e.ParentID != nil {
			parent, ok := byID[*e.ParentID]
			if !ok {
				return nil, ErrInvalidParent
			}
			if parent.UserID != userID || parent.Type != c.Type ||
				parent.IsSystem {
				return nil, ErrInvalidParent
			}
		}
		postParent[e.ID] = e.ParentID
	}

	for id := range postParent {
		seen := make(map[uuid.UUID]struct{})
		seen[id] = struct{}{}
		curr := postParent[id]
		hops := 1 // self counts as level 1
		for curr != nil {
			if _, dup := seen[*curr]; dup {
				return nil, ErrCycleDetected
			}
			seen[*curr] = struct{}{}
			hops++
			if hops > maxDepth {
				return nil, ErrMaxDepth
			}
			curr = postParent[*curr]
		}
	}

	type sibKey struct {
		typ      string
		parentID string // "" for null
		nameLow  string
	}
	groups := make(map[sibKey]int, len(all))
	for i := range all {
		c := &all[i]
		key := sibKey{typ: c.Type, nameLow: strings.ToLower(c.Name)}
		if p := postParent[c.ID]; p != nil {
			key.parentID = p.String()
		}
		groups[key]++
		if groups[key] > 1 {
			return nil, ErrDuplicateName
		}
	}

	out, err := s.store.ReorderTx(ctx, userID, entries)
	if err != nil {
		return nil, err
	}
	return s.decorate(ctx, userID, out)
}

func (s *Service) countTransactions(ctx context.Context, categoryID uuid.UUID) (int, error) {
	if s.txCounter == nil {
		return 0, nil
	}
	return s.txCounter(ctx, categoryID)
}

func (s *Service) validateParent(ctx context.Context, userID uuid.UUID, parentID *uuid.UUID, expectedType string) error {
	if parentID == nil {
		return nil
	}
	parent, err := s.store.GetByID(ctx, userID, *parentID)
	if err != nil {
		if errors.Is(err, ErrCategoryNotFound) {
			return ErrInvalidParent
		}
		return err
	}
	if parent.Type != expectedType {
		return ErrInvalidParent
	}
	if parent.IsSystem {
		return ErrInvalidParent
	}
	return nil
}

func (s *Service) subtreeDepth(ctx context.Context, id uuid.UUID) (int, error) {
	type level struct {
		ids []uuid.UUID
	}
	curr := []uuid.UUID{id}
	depth := 1
	for depth < maxDepth {
		rows, err := s.store.db.Query(ctx,
			`SELECT id FROM categories WHERE parent_id = ANY($1)`, curr)
		if err != nil {
			return 0, fmt.Errorf("subtree depth: %w", err)
		}
		next := []uuid.UUID{}
		for rows.Next() {
			var cid uuid.UUID
			if err := rows.Scan(&cid); err != nil {
				rows.Close()
				return 0, err
			}
			next = append(next, cid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		if len(next) == 0 {
			return depth, nil
		}
		depth++
		curr = next
	}
	return depth, nil
}

// decorate fills depth and path (ancestors root -> parent) on every row from ONE tree query, so a
// list never walks parents per row.
func (s *Service) decorate(ctx context.Context, userID uuid.UUID, cats []Category) ([]Category, error) {
	if len(cats) == 0 {
		return cats, nil
	}
	tree, err := s.store.TreeNames(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range cats {
		path := []PathItem{}
		for p, hops := cats[i].ParentID, 0; p != nil && hops < 8; hops++ {
			n, ok := tree[*p]
			if !ok {
				break
			}
			path = append([]PathItem{{ID: *p, Name: n.Name, IconCode: n.IconCode}}, path...)
			p = n.ParentID
		}
		cats[i].Path = path
		cats[i].Depth = len(path) + 1
	}
	return cats, nil
}

func (s *Service) decorateOne(ctx context.Context, userID uuid.UUID, c *Category) (*Category, error) {
	out, err := s.decorate(ctx, userID, []Category{*c})
	if err != nil {
		return nil, err
	}
	*c = out[0]
	return c, nil
}
