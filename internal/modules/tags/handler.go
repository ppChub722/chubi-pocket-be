package tags

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

// POST /v1/tags
func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	var req CreateTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	t, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, t)
}

// GET /v1/tags
func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	tags, err := h.service.List(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to list tags", err.Error())
		return
	}
	c.JSON(http.StatusOK, ListResponse{Data: tags})
}

// GET /v1/tags/:id
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

	t, err := h.service.Get(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, t)
}

// PUT /v1/tags/:id
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

	var req UpdateTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	t, err := h.service.Update(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, t)
}

// POST /v1/transactions/:id/tags
func (h *Handler) Attach(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	txID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid transaction id", nil)
		return
	}

	var req AttachRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	tags, err := h.service.AttachTags(c.Request.Context(), userID, txID, req.TagIDs)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, ListResponse{Data: tags})
}

// DELETE /v1/transactions/:id/tags/:tag_id
func (h *Handler) Detach(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	txID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid transaction id", nil)
		return
	}
	tagID, err := uuid.Parse(c.Param("tag_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid tag id", nil)
		return
	}

	if err := h.service.DetachTag(c.Request.Context(), userID, txID, tagID); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Tag removed from transaction", nil)
}

// DELETE /v1/tags/:id
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
	response.OK(c, "Tag deleted", nil)
}

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid tag id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrTagNotFound):
		response.NotFound(c, "NOT_FOUND", "Tag not found")
	case errors.Is(err, ErrTagExists):
		response.Fail(c, http.StatusConflict, "TAG_EXISTS", err.Error(), nil)
	case errors.Is(err, ErrTransactionNotOwned):
		response.NotFound(c, "NOT_FOUND", "Transaction not found")
	default:
		response.InternalError(c, "Tag operation failed", err.Error())
	}
}
