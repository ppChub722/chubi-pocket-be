package categories

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// POST /v1/categories
func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	var req CreateCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	cat, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, cat)
}

// GET /v1/categories
func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	f := ListFilter{Status: c.DefaultQuery("status", "active")}
	if t := c.Query("type"); t != "" {
		if t != "income" && t != "expense" {
			response.BadRequest(c, "VALIDATION_ERROR", "type must be 'income' or 'expense'", nil)
			return
		}
		f.Type = &t
	}
	if f.Status != "active" && f.Status != "archived" && f.Status != "all" {
		response.BadRequest(c, "VALIDATION_ERROR", "status must be 'active', 'archived', or 'all'", nil)
		return
	}
	if v := c.Query("include_system"); v == "true" || v == "1" {
		f.IncludeSystem = true
	}

	cats, err := h.service.List(c.Request.Context(), userID, f)
	if err != nil {
		response.InternalError(c, "Failed to list categories", err.Error())
		return
	}
	c.JSON(http.StatusOK, ListResponse{Data: cats})
}

// GET /v1/categories/:id
func (h *Handler) Get(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	detail, err := h.service.Get(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// PUT /v1/categories/:id
func (h *Handler) Update(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req UpdateCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	cat, err := h.service.Update(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, cat)
}

// DELETE /v1/categories/:id
func (h *Handler) Delete(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	status, err := h.service.Delete(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}

	msg := "Category deleted"
	if status == "archived" {
		msg = "Category archived"
	}
	c.JSON(http.StatusOK, gin.H{"message": msg, "status": status})
}

// POST /v1/categories/:id/restore
func (h *Handler) Restore(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	cat, err := h.service.Restore(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, cat)
}

// DELETE /v1/categories/:id/permanent
func (h *Handler) PermanentDelete(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	if err := h.service.PermanentDelete(c.Request.Context(), userID, id); err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Category permanently deleted"})
}

// --- Helpers ---

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid category id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrCategoryNotFound):
		response.NotFound(c, "NOT_FOUND", "Category not found")
	case errors.Is(err, ErrDuplicateName):
		response.BadRequest(c, "DUPLICATE_NAME", err.Error(), nil)
	case errors.Is(err, ErrInvalidParent):
		response.BadRequest(c, "INVALID_PARENT", err.Error(), nil)
	case errors.Is(err, ErrCycleDetected):
		response.BadRequest(c, "CYCLE_DETECTED", err.Error(), nil)
	case errors.Is(err, ErrMaxDepth):
		response.BadRequest(c, "MAX_DEPTH_EXCEEDED", err.Error(), nil)
	case errors.Is(err, ErrSystemImmutable):
		response.BadRequest(c, "SYSTEM_CATEGORY_IMMUTABLE", err.Error(), nil)
	case errors.Is(err, ErrNotArchived):
		response.BadRequest(c, "NOT_ARCHIVED", err.Error(), nil)
	case errors.Is(err, ErrHasTransactions):
		response.BadRequest(c, "HAS_TRANSACTIONS", err.Error(), nil)
	default:
		response.InternalError(c, "Category operation failed", err.Error())
	}
}
