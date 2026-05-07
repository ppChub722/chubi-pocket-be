package scheduled_transactions

import (
	"context"
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

// POST /v1/scheduled-transactions
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

// GET /v1/scheduled-transactions
func (h *Handler) List(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	f := ListFilter{
		Status:    c.Query("status"),
		EntryType: c.Query("entry_type"),
		Type:      c.Query("type"),
	}
	if v := c.Query("account_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid account_id", nil)
			return
		}
		f.AccountID = &id
	}
	out, err := h.service.List(c.Request.Context(), userID, f)
	if err != nil {
		response.InternalError(c, "List failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/scheduled-transactions/upcoming
func (h *Handler) Upcoming(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	days := atoiOr(c.Query("days"), 7)
	out, err := h.service.Upcoming(c.Request.Context(), userID, days)
	if err != nil {
		response.InternalError(c, "Upcoming failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/scheduled-transactions/:id
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

// PUT /v1/scheduled-transactions/:id
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

// DELETE /v1/scheduled-transactions/:id
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
	response.OK(c, "Scheduled transaction deleted", nil)
}

// POST /v1/scheduled-transactions/:id/pause
func (h *Handler) Pause(c *gin.Context) {
	h.transitionHandler(c, h.service.Pause)
}

// POST /v1/scheduled-transactions/:id/resume
func (h *Handler) Resume(c *gin.Context) {
	h.transitionHandler(c, h.service.Resume)
}

// POST /v1/scheduled-transactions/:id/cancel
func (h *Handler) Cancel(c *gin.Context) {
	h.transitionHandler(c, h.service.Cancel)
}

func (h *Handler) transitionHandler(
	c *gin.Context,
	fn func(ctx context.Context, userID, id uuid.UUID) (*ScheduledTransactionView, error),
) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := fn(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/scheduled-transactions/:id/generate-now
func (h *Handler) GenerateNow(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.GenerateNow(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/scheduled-transactions/:id/history
func (h *Handler) History(c *gin.Context) {
	userID, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.History(c.Request.Context(), userID, id)
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
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid scheduled transaction id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func atoiOr(s string, dflt int) int {
	if s == "" {
		return dflt
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return dflt
	}
	return n
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrScheduleNotFound):
		response.NotFound(c, "NOT_FOUND", "Scheduled transaction not found")
	case errors.Is(err, ErrAccountNotOwned):
		response.BadRequest(c, "ACCOUNT_NOT_OWNED", "Account does not belong to caller or is not active", nil)
	case errors.Is(err, ErrInvalidCategory):
		response.BadRequest(c, "INVALID_CATEGORY", "Category is invalid (system, wrong type, or not owned)", nil)
	case errors.Is(err, ErrInstallmentFieldsRequired):
		response.BadRequest(c, "INSTALLMENT_FIELDS_REQUIRED", "Installments require total_amount, total_installments, remaining_installments", nil)
	case errors.Is(err, ErrRecurringExtraFields):
		response.BadRequest(c, "RECURRING_EXTRA_FIELDS", "Recurring entries must not include installment-only fields", nil)
	case errors.Is(err, ErrRemainingExceedsTotal):
		response.BadRequest(c, "VALIDATION_ERROR", "remaining_installments cannot exceed total_installments", nil)
	case errors.Is(err, ErrInvalidTransition):
		response.BadRequest(c, "INVALID_TRANSITION", "Invalid status transition for this schedule", nil)
	case errors.Is(err, ErrTerminalStatus):
		response.BadRequest(c, "INVALID_TRANSITION", "Schedule is in a terminal status", nil)
	default:
		response.InternalError(c, "Scheduled transaction operation failed", err.Error())
	}
}
