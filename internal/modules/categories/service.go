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

// TransactionCounter is injected by main.go (wired to transactions.Store.CountByCategory)
// so that DELETE /v1/categories/:id can decide between archive and hard delete.
// nil = no counter wired yet (Phase 1a.1) → behaves as 0 (always hard-delete).
type TransactionCounter func(ctx context.Context, categoryID uuid.UUID) (int, error)

type Service struct {
	store     *Store
	txCounter TransactionCounter
}

func NewService(s *Store) *Service {
	return &Service{store: s}
}

// WithTransactionCounter wires the cross-module count callback. main.go calls
// this after both transactions and categories services are constructed.
func (s *Service) WithTransactionCounter(c TransactionCounter) {
	s.txCounter = c
}

// Seeder is the public surface that auth.Service calls inside its register tx.
type Seeder interface {
	SeedForUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error
}

// SeedForUser implements Seeder. The Service exposes this method so the auth
// module can depend on the interface, not the concrete struct.
func (s *Service) SeedForUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	return s.store.SeedForUserTx(ctx, tx, userID)
}

// SystemFor is the public lookup other modules call (transactions, accounts).
// Returns the user's category for `kind`. Cached per-request by callers if hot.
func (s *Service) SystemFor(ctx context.Context, userID uuid.UUID, kind SystemKind) (*Category, error) {
	return s.store.LookupSystem(ctx, userID, kind)
}

// GetCategoryRow returns the bare Category row (no depth / counts). Used by
// transactions.Service for validating user-supplied category_id without the
// overhead of the detail computation.
func (s *Service) GetCategoryRow(ctx context.Context, userID, id uuid.UUID) (*Category, error) {
	return s.store.GetByID(ctx, userID, id)
}

// --- CRUD ---

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
	c := &Category{
		UserID:   userID,
		Name:     strings.TrimSpace(req.Name),
		Type:     req.Type,
		ParentID: req.ParentID,
		IsSystem: false,
		Icon:     req.Icon,
		Color:    req.Color,
	}
	return s.store.Create(ctx, c)
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*CategoryDetail, error) {
	c, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	depth, err := s.store.DepthFromParent(ctx, c.ParentID)
	if err != nil {
		return nil, err
	}
	childCount, err := s.store.CountChildren(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("count children: %w", err)
	}
	txCount, err := s.countTransactions(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("count transactions: %w", err)
	}

	out := &CategoryDetail{
		Category:         *c,
		Depth:            depth,
		ChildCount:       childCount,
		TransactionCount: txCount,
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
	if f.Status == "" {
		f.Status = "active"
	}
	return s.store.List(ctx, userID, f)
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateCategoryRequest) (*Category, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if current.IsSystem {
		// Per spec §4.14 the display `name` may be renamed for personalization
		// / i18n. parent_id, icon, color also editable. Type is immutable.
		// (Phase 1a doesn't otherwise restrict; system identity is system_kind,
		// not the name string.)
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
			// Depth check: depth(newParent) + 1 + max-subtree-depth-under-id ≤ 3.
			// Simplified Phase 1a: just enforce depth(newParent) + 1 ≤ 3, which
			// works as long as `id` itself has no children (most renames). If
			// `id` has children, the children's depth becomes
			// depth(newParent) + 2 — caller must move children separately or
			// we reject. Enforce strictly: subtree depth of `id` + new parent's
			// depth ≤ maxDepth.
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

	return s.store.Update(ctx, userID, id, name, newParent, parentChanged,
		req.Icon, req.Color)
}

// Delete archives or hard-deletes per spec §3.5.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) (status string, err error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return "", err
	}
	if current.IsSystem {
		return "", fmt.Errorf("%w: system categories cannot be deleted", ErrSystemImmutable)
	}

	if err := s.store.ReparentChildren(ctx, userID, id); err != nil {
		return "", err
	}

	txCount, err := s.countTransactions(ctx, id)
	if err != nil {
		return "", err
	}
	if txCount == 0 {
		if err := s.store.HardDelete(ctx, userID, id); err != nil {
			return "", err
		}
		return "deleted", nil
	}

	if err := s.store.SetStatus(ctx, userID, id, "archived"); err != nil {
		return "", err
	}
	return "archived", nil
}

// Restore reactivates an archived category. Auto-reparents to nearest active
// ancestor if the original parent is still archived (spec §4.8).
func (s *Service) Restore(ctx context.Context, userID, id uuid.UUID) (*Category, error) {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if current.Status != "archived" {
		return nil, ErrNotArchived
	}

	// If parent is archived, find nearest active ancestor.
	if current.ParentID != nil {
		parent, err := s.store.GetByID(ctx, userID, *current.ParentID)
		if err != nil && !errors.Is(err, ErrCategoryNotFound) {
			return nil, err
		}
		if parent == nil || parent.Status != "active" {
			active, err := s.store.FindNearestActiveAncestor(ctx, id)
			if err != nil {
				return nil, err
			}
			// active may be nil → becomes root
			if _, err := s.store.Update(ctx, userID, id, nil, active, true, nil, nil); err != nil {
				return nil, err
			}
		}
	}

	if err := s.store.SetStatus(ctx, userID, id, "active"); err != nil {
		return nil, err
	}
	return s.store.GetByID(ctx, userID, id)
}

// PermanentDelete hard-deletes from archived state with 0 transactions.
func (s *Service) PermanentDelete(ctx context.Context, userID, id uuid.UUID) error {
	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return err
	}
	if current.IsSystem {
		return fmt.Errorf("%w: system categories cannot be deleted", ErrSystemImmutable)
	}
	if current.Status != "archived" {
		return ErrNotArchived
	}
	txCount, err := s.countTransactions(ctx, id)
	if err != nil {
		return err
	}
	if txCount > 0 {
		return ErrHasTransactions
	}
	if err := s.store.ReparentChildren(ctx, userID, id); err != nil {
		return err
	}
	return s.store.HardDelete(ctx, userID, id)
}

// --- Internal helpers ---

// countTransactions consults the wired counter. nil counter (1a.1 only) → 0.
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
	if parent.Status != "active" {
		return ErrInvalidParent
	}
	if parent.IsSystem {
		return ErrInvalidParent
	}
	return nil
}

// subtreeDepth returns 1 if `id` has no children, 2 if it has children but no
// grandchildren, 3 if it has grandchildren. Used for depth check on parent
// change so children/grandchildren don't break the 3-layer limit.
func (s *Service) subtreeDepth(ctx context.Context, id uuid.UUID) (int, error) {
	// Phase 1a tree is bounded at 3 → a few SQL hops cover it.
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
