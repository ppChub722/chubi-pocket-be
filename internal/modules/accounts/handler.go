package accounts

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
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

// POST /v1/accounts
func (h *Handler) Create(c *gin.Context) {
	user, ok := auth.UserFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	a, err := h.service.Create(c.Request.Context(), user.ID, user.Currency, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, a)
}

// GET /v1/accounts
func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	status := c.DefaultQuery("status", StatusActive)
	if status != StatusActive && status != StatusArchived && status != StatusClosed && status != "all" {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid status", nil)
		return
	}
	accType := c.Query("type")

	accs, err := h.service.List(c.Request.Context(), userID, status, accType)
	if err != nil {
		response.InternalError(c, "Failed to list accounts", err.Error())
		return
	}
	c.JSON(http.StatusOK, ListResponse{Data: accs, Total: len(accs)})
}

// GET /v1/accounts/:id
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

	a, err := h.service.Get(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

// PUT /v1/accounts/:id
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

	// Reject `balance` in body explicitly per spec §3.4. ShouldBindBodyWith
	// caches the body so we can probe for the field name and then bind to
	// the typed struct without re-reading the request.
	var peek map[string]any
	if err := c.ShouldBindBodyWith(&peek, binding.JSON); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if _, hasBalance := peek["balance"]; hasBalance {
		response.BadRequest(c, "BALANCE_NOT_EDITABLE", ErrBalanceNotEditable.Error(), nil)
		return
	}
	var req UpdateRequest
	if err := c.ShouldBindBodyWith(&req, binding.JSON); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	a, err := h.service.Update(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

// POST /v1/accounts/:id/adjust-balance
func (h *Handler) AdjustBalance(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req AdjustBalanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if req.Date == nil {
		t := time.Now().Format("2006-01-02")
		req.Date = &t
	}

	res, err := h.service.AdjustBalance(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// DELETE /v1/accounts/:id (always soft → archived)
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

	if err := h.service.Archive(c.Request.Context(), userID, id); err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Account archived", "status": "archived"})
}

// GET /v1/accounts/:id/summary
func (h *Handler) Summary(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	from := c.Query("from")
	to := c.Query("to")
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}
	if from == "" {
		from = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	}

	res, err := h.service.Summary(c.Request.Context(), userID, id, from, to)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// --- Helpers ---

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid account id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrAccountNotFound):
		response.NotFound(c, "NOT_FOUND", "Account not found")
	case errors.Is(err, ErrCreditFieldMismatch):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrTypeChangeNeedsLimit):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrBalanceNotEditable):
		response.BadRequest(c, "BALANCE_NOT_EDITABLE", err.Error(), nil)
	case errors.Is(err, ErrAdjustNoOp):
		response.BadRequest(c, "NO_OP", err.Error(), nil)
	default:
		response.InternalError(c, "Account operation failed", err.Error())
	}
}
