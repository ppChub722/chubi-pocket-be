package personal_debts

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

// GET /v1/personal-debts/people
// Aggregated by counterparty with net position. Spec §12.5 dashboard.
func (h *Handler) People(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	out, err := h.service.People(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "People view failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/personal-debts
func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	f := ListFilter{
		Direction: c.Query("direction"),
		Status:    c.Query("status"),
		Page:      atoiOr(c.Query("page"), 1),
		PerPage:   atoiOr(c.Query("per_page"), 20),
	}
	if v := c.Query("counterparty_contact_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid counterparty_contact_id", nil)
			return
		}
		f.CounterpartyContactID = &id
	}
	if v := c.Query("from"); v != "" {
		f.From = &v
	}
	if v := c.Query("to"); v != "" {
		f.To = &v
	}
	out, err := h.service.List(c.Request.Context(), userID, f)
	if err != nil {
		response.InternalError(c, "List failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/personal-debts (manual create)
func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
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

// GET /v1/personal-debts/:id
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
	out, err := h.service.Get(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// PUT /v1/personal-debts/:id (direct edit — barter, forgiveness, math fix)
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

// DELETE /v1/personal-debts/:id
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
	if err := h.service.Delete(c.Request.Context(), userID, id); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Personal debt deleted", nil)
}

// POST /v1/personal-debts/:id/cancel
func (h *Handler) Cancel(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Cancel(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/personal-debts/:id/settle
// Records a settlement event. By default creates a transaction (account_id
// must be supplied). When `?direct=true` is set, skips the transaction
// and just bumps settled_amount — for forgiveness/barter/no-cash cases.
func (h *Handler) Settle(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req SettleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	directOnly := c.Query("direct") == "true"
	recordTx := !directOnly && req.AccountID != uuid.Nil
	out, err := h.service.Settle(c.Request.Context(), userID, id, req, recordTx)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid debt id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrDebtNotFound):
		response.NotFound(c, "NOT_FOUND", "Personal debt not found")
	case errors.Is(err, ErrAlreadySettled):
		response.BadRequest(c, "ALREADY_SETTLED", "Debt is already settled", nil)
	case errors.Is(err, ErrAlreadyCancelled):
		response.BadRequest(c, "ALREADY_CANCELLED", "Debt is already cancelled", nil)
	case errors.Is(err, ErrOverpayment):
		response.BadRequest(c, "OVERPAYMENT", "Settle amount exceeds outstanding", nil)
	default:
		response.InternalError(c, "Personal debt operation failed", err.Error())
	}
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
