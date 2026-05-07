package budgets

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

const headerTimezone = "X-Timezone"

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// POST /v1/budgets
func (h *Handler) Create(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.Create(c.Request.Context(), userID, req, c.GetHeader(headerTimezone))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// GET /v1/budgets
func (h *Handler) List(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	f := ListFilter{
		Status: c.Query("status"),
		Period: c.Query("period"),
		Scope:  c.Query("scope"),
	}
	if v := c.Query("project_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid project_id", nil)
			return
		}
		f.ProjectID = &id
	}
	out, err := h.service.List(c.Request.Context(), userID, f, c.GetHeader(headerTimezone))
	if err != nil {
		response.InternalError(c, "List failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/budgets/:id
func (h *Handler) Get(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Get(c.Request.Context(), userID, id, c.GetHeader(headerTimezone))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// PUT /v1/budgets/:id
func (h *Handler) Update(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req UpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.Update(c.Request.Context(), userID, id, req, c.GetHeader(headerTimezone))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /v1/budgets/:id
func (h *Handler) Delete(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), userID, id); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Budget deleted", nil)
}

// POST /v1/budgets/:id/archive
func (h *Handler) Archive(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Archive(c.Request.Context(), userID, id, c.GetHeader(headerTimezone))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/budgets/:id/restore
func (h *Handler) Restore(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Restore(c.Request.Context(), userID, id, c.GetHeader(headerTimezone))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/budgets/overview
func (h *Handler) Overview(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	period := c.DefaultQuery("period", PeriodMonthly)
	scope := c.DefaultQuery("scope", ScopeUser)
	var projectID *uuid.UUID
	if v := c.Query("project_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid project_id", nil)
			return
		}
		projectID = &id
	}
	if scope == ScopeProject && projectID == nil {
		response.BadRequest(c, "VALIDATION_ERROR", "project_id required when scope=project", nil)
		return
	}
	out, err := h.service.Overview(c.Request.Context(), userID, period, scope, projectID, c.GetHeader(headerTimezone))
	if err != nil {
		response.InternalError(c, "Overview failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// --- helpers ---

func requireUser(c *gin.Context) (uuid.UUID, bool) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return uuid.Nil, false
	}
	return userID, true
}

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid budget id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrBudgetNotFound):
		response.NotFound(c, "NOT_FOUND", "Budget not found")
	case errors.Is(err, ErrDuplicateBudget):
		response.BadRequest(c, "DUPLICATE_BUDGET", "An active budget already exists for this category/period/scope", nil)
	case errors.Is(err, ErrInvalidCategory):
		response.BadRequest(c, "INVALID_CATEGORY", "Category must be expense-typed, owned, and not a system category", nil)
	case errors.Is(err, ErrProjectNotOwner):
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "Only the project owner can manage project-scope budgets", nil)
	default:
		response.InternalError(c, "Budget operation failed", err.Error())
	}
}
