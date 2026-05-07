package saving_goals

import (
	"errors"
	"net/http"
	"strconv"

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

// POST /v1/saving-goals
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
	out, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// GET /v1/saving-goals
func (h *Handler) List(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	f := ListFilter{Status: c.Query("status")}
	if v := c.Query("account_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid account_id", nil)
			return
		}
		f.AccountID = &id
	}
	if v := c.Query("is_completed"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid is_completed", nil)
			return
		}
		f.IsCompleted = &b
	}
	out, err := h.service.List(c.Request.Context(), userID, f)
	if err != nil {
		response.InternalError(c, "List failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/saving-goals/:id
func (h *Handler) Get(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Get(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// PUT /v1/saving-goals/:id
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
	out, err := h.service.Update(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /v1/saving-goals/:id
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
	response.OK(c, "Saving goal deleted", nil)
}

// POST /v1/saving-goals/:id/archive
func (h *Handler) Archive(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Archive(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/saving-goals/:id/restore
func (h *Handler) Restore(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Restore(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/accounts/:id/saving-allocations
func (h *Handler) AccountAllocations(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid account id", nil)
		return
	}
	out, err := h.service.AccountAllocations(c.Request.Context(), userID, accountID)
	if err != nil {
		mapServiceError(c, err)
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
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid saving goal id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrGoalNotFound):
		response.NotFound(c, "NOT_FOUND", "Saving goal not found")
	case errors.Is(err, ErrAccountNotOwned):
		response.BadRequest(c, "ACCOUNT_NOT_OWNED", "Linked account does not belong to caller", nil)
	case errors.Is(err, ErrAllocationExceeded):
		response.BadRequest(c, "ALLOCATION_EXCEEDED", "Sum of allocations on account would exceed 100%", nil)
	default:
		response.InternalError(c, "Saving goal operation failed", err.Error())
	}
}
