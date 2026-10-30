package tags

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.BindError(c, err)
		return
	}
	var probe map[string]json.RawMessage
	_ = json.Unmarshal(raw, &probe)
	_, hasCreate := probe["create"]
	_, hasUpdate := probe["update"]
	_, hasDelete := probe["delete"]
	if hasCreate || hasUpdate || hasDelete {
		h.batch(c, userID, raw)
		return
	}

	var req CreateTagRequest
	if err := binding.JSON.BindBody(raw, &req); err != nil {
		response.BindError(c, err)
		return
	}

	t, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, t)
}

func (h *Handler) batch(c *gin.Context, userID uuid.UUID, raw []byte) {
	var req BatchRequest
	if err := binding.JSON.BindBody(raw, &req); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.Batch(c.Request.Context(), userID, req)
	if err != nil {
		var be *BatchError
		if errors.As(err, &be) {
			details := map[string]any{"op": be.Op, "index": be.Index}
			switch {
			case errors.Is(err, ErrTagNotFound):
				response.Fail(c, http.StatusNotFound, apierr.NotFound, "Tag not found", details)
			case errors.Is(err, ErrTagExists):
				response.Fail(c, http.StatusConflict, apierr.TagExists, ErrTagExists.Error(), details)
			default:
				response.InternalError(c, "Tag operation failed", err)
			}
			return
		}
		if errors.Is(err, ErrBatchInvalid) {
			response.BindError(c, err)
			return
		}
		response.InternalError(c, "Tag operation failed", err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	tags, err := h.service.List(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to list tags", err)
		return
	}
	c.JSON(http.StatusOK, ListResponse{Data: tags})
}

func (h *Handler) Update(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req UpdateTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}

	t, err := h.service.Update(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, t)
}

func (h *Handler) Delete(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
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
	c.JSON(http.StatusOK, DeletedResult{ID: id})
}

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Validation(c, "Invalid tag id", apierr.Field("id", apierr.FieldInvalid))
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrTagNotFound):
		response.NotFound(c, apierr.NotFound, "Tag not found")
	case errors.Is(err, ErrTagExists):
		response.Fail(c, http.StatusConflict, apierr.TagExists, err.Error(), nil)
	case errors.Is(err, ErrTransactionNotOwned):
		response.NotFound(c, apierr.NotFound, "Transaction not found")
	default:
		response.InternalError(c, "Tag operation failed", err)
	}
}
