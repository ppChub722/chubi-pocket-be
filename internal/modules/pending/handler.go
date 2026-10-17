package pending

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

func NewHandler(s *Service) *Handler { return &Handler{service: s} }

func bindUser(c *gin.Context) (uuid.UUID, bool) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
	}
	return userID, ok
}

func bindID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid pending transaction id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrPendingNotFound):
		response.NotFound(c, "NOT_FOUND", "Pending transaction not found")
	case errors.Is(err, ErrInvalidDraft):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	default:
		response.InternalError(c, "Pending transaction operation failed", err.Error())
	}
}

// GET /v1/pending-transactions
func (h *Handler) List(c *gin.Context) {
	userID, ok := bindUser(c)
	if !ok {
		return
	}
	out, err := h.service.List(c.Request.Context(), userID)
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/pending-transactions — one or many manual drafts.
func (h *Handler) Create(c *gin.Context) {
	userID, ok := bindUser(c)
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
		mapError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": out})
}

// PUT /v1/pending-transactions/:id — replace the draft.
func (h *Handler) Update(c *gin.Context) {
	userID, ok := bindUser(c)
	if !ok {
		return
	}
	id, ok := bindID(c)
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
		mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /v1/pending-transactions/:id — discard.
func (h *Handler) Delete(c *gin.Context) {
	userID, ok := bindUser(c)
	if !ok {
		return
	}
	id, ok := bindID(c)
	if !ok {
		return
	}
	out, err := h.service.Delete(c.Request.Context(), userID, id)
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/pending-transactions/discard — many at once; unknown ids are skipped.
func (h *Handler) Discard(c *gin.Context) {
	userID, ok := bindUser(c)
	if !ok {
		return
	}
	var req DiscardRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.Discard(c.Request.Context(), userID, req.IDs)
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/pending-transactions/submit — each draft independently.
func (h *Handler) Submit(c *gin.Context) {
	userID, ok := bindUser(c)
	if !ok {
		return
	}
	var req SubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.Submit(c.Request.Context(), userID, req)
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/pending-transactions/:id/submit — confirm one draft (same answer as
// the bulk submit, with that one id).
func (h *Handler) SubmitOne(c *gin.Context) {
	userID, ok := bindUser(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid id", nil)
		return
	}
	out, err := h.service.Submit(c.Request.Context(), userID, SubmitRequest{IDs: []uuid.UUID{id}})
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/pending-transactions/submit-ready — confirm every draft that needs no input.
func (h *Handler) SubmitReady(c *gin.Context) {
	userID, ok := bindUser(c)
	if !ok {
		return
	}
	out, err := h.service.SubmitReady(c.Request.Context(), userID)
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
