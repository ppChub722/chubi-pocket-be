package institutions

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct{ service *Service }

func NewHandler(s *Service) *Handler { return &Handler{service: s} }

func ids(c *gin.Context, withID bool) (uuid.UUID, uuid.UUID, bool) {
	uid, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return uuid.Nil, uuid.Nil, false
	}
	if !withID {
		return uid, uuid.Nil, true
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Validation(c, "Invalid id", apierr.Field("id", apierr.FieldInvalid))
		return uuid.Nil, uuid.Nil, false
	}
	return uid, id, true
}

func reply(c *gin.Context, status int, out any, err error) {
	switch {
	case err == nil:
		c.JSON(status, out)
	case errors.Is(err, ErrNotFound):
		response.NotFound(c, apierr.InstitutionNotFound, "Institution not found")
	case errors.Is(err, ErrInUse):
		response.Fail(c, http.StatusConflict, apierr.InstitutionInUse, err.Error(), nil)
	case errors.Is(err, ErrProviderNotFound):
		response.Validation(c, "Unknown provider_id", apierr.Field("provider_id", apierr.FieldInvalid))
	case errors.Is(err, ErrInvalidState):
		response.Fail(c, http.StatusConflict, apierr.InvalidTransition, err.Error(), nil)
	case errors.Is(err, ErrValidation):
		response.ValidationErr(c, err)
	default:
		response.InternalError(c, "Institution operation failed", err)
	}
}

func (h *Handler) List(c *gin.Context) {
	uid, _, ok := ids(c, false)
	if !ok {
		return
	}
	status := c.DefaultQuery("status", "active")
	if status != "active" && status != "archived" && status != "all" {
		response.Validation(c, "Invalid status", apierr.Field("status", apierr.FieldInvalid))
		return
	}
	out, err := h.service.List(c.Request.Context(), uid, status)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Create(c *gin.Context) {
	uid, _, ok := ids(c, false)
	if !ok {
		return
	}
	var in Input
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BindError(c, err)
		return
	}
	out, created, err := h.service.Create(c.Request.Context(), uid, in)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	reply(c, status, out, err)
}

func (h *Handler) Get(c *gin.Context) {
	uid, id, ok := ids(c, true)
	if !ok {
		return
	}
	out, err := h.service.Get(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Update(c *gin.Context) {
	uid, id, ok := ids(c, true)
	if !ok {
		return
	}
	var in Input
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.Update(c.Request.Context(), uid, id, in)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Archive(c *gin.Context) {
	uid, id, ok := ids(c, true)
	if !ok {
		return
	}
	out, err := h.service.Archive(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Restore(c *gin.Context) {
	uid, id, ok := ids(c, true)
	if !ok {
		return
	}
	out, err := h.service.Restore(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Delete(c *gin.Context) {
	uid, id, ok := ids(c, true)
	if !ok {
		return
	}
	out, err := h.service.Delete(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}
