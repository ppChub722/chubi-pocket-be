package pending

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler { return &Handler{service: s} }

func bindUser(c *gin.Context) (uuid.UUID, bool) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
	}
	return userID, ok
}

func bindID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Validation(c, "Invalid pending transaction id", apierr.Field("id", apierr.FieldInvalid))
		return uuid.Nil, false
	}
	return id, true
}

func mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrPendingNotFound):
		response.NotFound(c, apierr.NotFound, "Pending transaction not found")
	case errors.Is(err, ErrInvalidDraft):
		response.ValidationErr(c, err)
	default:
		response.InternalError(c, "Pending transaction operation failed", err)
	}
}

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

func (h *Handler) Create(c *gin.Context) {
	userID, ok := bindUser(c)
	if !ok {
		return
	}
	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapError(c, err)
		return
	}
	sum, err := h.service.freshSummary(c.Request.Context(), userID)
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": out, "summary": sum})
}

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
		response.BindError(c, err)
		return
	}
	out, err := h.service.Update(c.Request.Context(), userID, id, req)
	if err != nil {
		mapError(c, err)
		return
	}
	sum, err := h.service.freshSummary(c.Request.Context(), userID)
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, UpdateResponse{PendingTransaction: out, Summary: sum})
}

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

func (h *Handler) Discard(c *gin.Context) {
	userID, ok := bindUser(c)
	if !ok {
		return
	}
	var req DiscardRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.Discard(c.Request.Context(), userID, req.IDs)
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Submit(c *gin.Context) {
	userID, ok := bindUser(c)
	if !ok {
		return
	}
	var req SubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.Submit(c.Request.Context(), userID, req)
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) SubmitOne(c *gin.Context) {
	userID, ok := bindUser(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Validation(c, "Invalid id", apierr.Field("id", apierr.FieldInvalid))
		return
	}
	out, err := h.service.Submit(c.Request.Context(), userID, SubmitRequest{IDs: []uuid.UUID{id}})
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

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
