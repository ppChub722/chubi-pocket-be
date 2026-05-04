package notifications

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

// GET /v1/notifications
func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	f := ListFilter{
		Read:     c.Query("read"),
		Actioned: c.Query("actioned"),
		Type:     c.Query("type"),
		Page:     atoiOr(c.Query("page"), 1),
		PerPage:  atoiOr(c.Query("per_page"), 20),
	}
	if v := c.Query("from"); v != "" {
		f.From = &v
	}
	if v := c.Query("to"); v != "" {
		f.To = &v
	}
	out, err := h.service.List(c.Request.Context(), userID, f)
	if err != nil {
		response.InternalError(c, "Failed to list notifications", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/notifications/read-all
func (h *Handler) ReadAll(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	n, err := h.service.MarkReadAll(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Mark all read failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"marked_read": n})
}

// POST /v1/notifications/:id/read
func (h *Handler) MarkRead(c *gin.Context) {
	userID, id, ok := h.bind(c)
	if !ok {
		return
	}
	out, err := h.service.MarkRead(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/notifications/:id/actioned
func (h *Handler) MarkActioned(c *gin.Context) {
	userID, id, ok := h.bind(c)
	if !ok {
		return
	}
	out, err := h.service.MarkActioned(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/notifications/:id/dismiss
func (h *Handler) MarkDismissed(c *gin.Context) {
	userID, id, ok := h.bind(c)
	if !ok {
		return
	}
	out, err := h.service.MarkDismissed(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /v1/notifications/:id
func (h *Handler) Delete(c *gin.Context) {
	userID, id, ok := h.bind(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), userID, id); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Notification deleted", nil)
}

// GET /v1/notifications/settings
func (h *Handler) GetSettings(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	out, err := h.service.GetSettings(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to get settings", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// PUT /v1/notifications/settings
func (h *Handler) UpdateSettings(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	var req UpdateSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.UpdateSettings(c.Request.Context(), userID, req)
	if err != nil {
		response.InternalError(c, "Failed to update settings", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) bind(c *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid notification id", nil)
		return uuid.Nil, uuid.Nil, false
	}
	return userID, id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotificationNotFound):
		response.NotFound(c, "NOT_FOUND", "Notification not found")
	case errors.Is(err, ErrNotForYou):
		response.Fail(c, http.StatusForbidden, "NOT_FOR_YOU", "Notification is not addressed to caller", nil)
	default:
		response.InternalError(c, "Notification operation failed", err.Error())
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
