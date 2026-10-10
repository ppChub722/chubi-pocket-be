package personal_debts

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
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
	if err := c.ShouldBindBodyWith(&req, binding.JSON); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	// Explicit `null` clears (contract §7); absent keys leave the field.
	var raw map[string]json.RawMessage
	if body, ok := c.Get(gin.BodyBytesKey); ok {
		_ = json.Unmarshal(body.([]byte), &raw)
	}
	isNull := func(k string) bool { v, ok := raw[k]; return ok && string(v) == "null" }
	req.ClearContact = isNull("counterparty_contact_id")
	req.ClearDescription = isNull("description")
	req.ClearNote = isNull("note")
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
// Records a payment against a debt: always a transaction — into
// account_id, or a floating (no-wallet) row when it is omitted.
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
	// Optional wallet: none = a floating transaction (contract §7, rev.
	// 2026-10-08). The old ?direct=true is ignored — settling always records.
	if req.AccountID != nil && *req.AccountID == uuid.Nil {
		req.AccountID = nil
	}
	out, err := h.service.Settle(c.Request.Context(), userID, id, req)
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
	case errors.Is(err, ErrDebtOverpaid):
		response.BadRequest(c, "DEBT_OVERPAID", err.Error(), nil)
	case errors.Is(err, ErrOverpayment):
		response.BadRequest(c, "OVERPAYMENT", "Settle amount exceeds outstanding", nil)
	case errors.Is(err, ErrInvalidSettled):
		response.BadRequest(c, "INVALID_SETTLED_AMOUNT", "settled_amount cannot exceed amount", nil)
	case errors.Is(err, ErrContactNotFound):
		response.BadRequest(c, "CONTACT_NOT_FOUND", "Contact not found", nil)
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

// POST /v1/personal-debts/split-requests/:notification_id/accept —
// "add to my debts" on a split_created notification (contract §5).
func (h *Handler) AcceptSplitRequest(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	nid, err := uuid.Parse(c.Param("notification_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid notification id", nil)
		return
	}
	out, err := h.service.AcceptSplitRequest(c.Request.Context(), userID, nid)
	switch {
	case errors.Is(err, ErrSplitChangeStale):
		response.Fail(c, http.StatusConflict, "SPLIT_CHANGE_STALE", err.Error(), nil)
	case errors.Is(err, ErrNotSplitRequest):
		response.BadRequest(c, "NOT_SPLIT_REQUEST", err.Error(), nil)
	case errors.Is(err, notifications.ErrNotificationNotFound):
		response.NotFound(c, "NOT_FOUND", "Notification not found")
	case errors.Is(err, notifications.ErrNotForYou):
		response.Fail(c, http.StatusForbidden, "NOT_FOR_YOU", "Notification is not addressed to caller", nil)
	case errors.Is(err, notifications.ErrStateConflict):
		response.Fail(c, http.StatusConflict, "NOTIFICATION_STATE_CONFLICT", err.Error(), nil)
	case err != nil:
		mapServiceError(c, err)
	default:
		c.JSON(http.StatusCreated, out)
	}
}

// POST /v1/personal-debts/split-changes/:notification_id/apply —
// "อัปเดตตาม" on a split_changed notification (one shot).
func (h *Handler) ApplySplitChange(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	nid, err := uuid.Parse(c.Param("notification_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid notification id", nil)
		return
	}
	out, err := h.service.ApplySplitChange(c.Request.Context(), userID, nid)
	switch {
	case errors.Is(err, ErrNotSplitChange):
		response.BadRequest(c, "NOT_SPLIT_CHANGE", err.Error(), nil)
	case errors.Is(err, ErrNotificationActioned):
		response.Fail(c, http.StatusConflict, "NOTIFICATION_ACTIONED", err.Error(), nil)
	case errors.Is(err, ErrSplitChangeStale):
		response.Fail(c, http.StatusConflict, "SPLIT_CHANGE_STALE", err.Error(), nil)
	case errors.Is(err, notifications.ErrNotificationNotFound):
		response.NotFound(c, "NOT_FOUND", "Notification not found")
	case errors.Is(err, notifications.ErrNotForYou):
		response.Fail(c, http.StatusForbidden, "NOT_FOR_YOU", "Notification is not addressed to caller", nil)
	case errors.Is(err, notifications.ErrStateConflict):
		response.Fail(c, http.StatusConflict, "NOTIFICATION_STATE_CONFLICT", err.Error(), nil)
	case err != nil:
		mapServiceError(c, err)
	default:
		c.JSON(http.StatusOK, out)
	}
}
