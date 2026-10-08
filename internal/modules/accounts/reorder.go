package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

// PATCH /v1/accounts/reorder (contract §3). The order is the caller's own
// (account_members.sort_order) — reordering a shared wallet never moves
// it for the other members.

var (
	ErrReorderForbidden = errors.New("reorder lists a wallet the caller is not an active member of")
	ErrReorderDuplicate = errors.New("reorder lists the same wallet twice")
)

type ReorderItem struct {
	ID        uuid.UUID `json:"id"         binding:"required"`
	SortOrder int       `json:"sort_order"`
}

type ReorderRequest struct {
	Items []ReorderItem `json:"items" binding:"required,dive"`
}

// ReorderTx applies every item in one transaction, or none.
func (s *Store) ReorderTx(ctx context.Context, userID uuid.UUID, items []ReorderItem) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, it := range items {
		tag, err := tx.Exec(ctx, `
			UPDATE account_members SET sort_order = $1, updated_at = NOW()
			WHERE account_id = $2 AND user_id = $3
			  AND joined_at IS NOT NULL AND left_at IS NULL`,
			it.SortOrder, it.ID, userID)
		if err != nil {
			return fmt.Errorf("reorder %s: %w", it.ID, err)
		}
		if tag.RowsAffected() == 0 {
			return ErrReorderForbidden
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) Reorder(ctx context.Context, userID uuid.UUID, items []ReorderItem) ([]Account, error) {
	seen := make(map[uuid.UUID]bool, len(items))
	for _, it := range items {
		if seen[it.ID] {
			return nil, ErrReorderDuplicate
		}
		seen[it.ID] = true
	}
	if err := s.store.ReorderTx(ctx, userID, items); err != nil {
		return nil, err
	}
	return s.List(ctx, userID, StatusActive, "")
}

func (h *Handler) Reorder(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	var req ReorderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.Reorder(c.Request.Context(), userID, req.Items)
	switch {
	case errors.Is(err, ErrReorderForbidden):
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", err.Error(), nil)
	case errors.Is(err, ErrReorderDuplicate):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case err != nil:
		mapServiceError(c, err)
	default:
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}
