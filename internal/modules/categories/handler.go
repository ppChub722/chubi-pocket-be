package categories

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

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	var req CreateCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}

	cat, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, cat)
}

func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	var f ListFilter
	if t := c.Query("type"); t != "" {
		if t != "income" && t != "expense" {
			response.Validation(c, "type must be 'income' or 'expense'", apierr.Field("type", apierr.FieldInvalid))
			return
		}
		f.Type = &t
	}
	if v := c.Query("include_system"); v == "true" || v == "1" {
		f.IncludeSystem = true
	}

	cats, err := h.service.ListMonth(c.Request.Context(), userID, f, c.GetHeader("X-Timezone"))
	if err != nil {
		response.InternalError(c, "Failed to list categories", err)
		return
	}
	c.JSON(http.StatusOK, ListResponse{Data: cats})
}

func (h *Handler) Get(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	detail, err := h.service.Get(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
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

	var req UpdateCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}

	cat, err := h.service.Update(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, cat)
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

	out, err := h.service.Delete(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Reorder(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	var req ReorderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}

	updated, err := h.service.Reorder(c.Request.Context(), userID, req.Categories)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, ListResponse{Data: updated})
}

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Validation(c, "Invalid category id", apierr.Field("id", apierr.FieldInvalid))
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrCategoryNotFound):
		response.NotFound(c, apierr.NotFound, "Category not found")
	case errors.Is(err, ErrDuplicateName):
		response.Fail(c, http.StatusUnprocessableEntity, apierr.CategoryNameDuplicate, err.Error(), map[string]string{"field": "name"})
	case errors.Is(err, ErrInvalidParent):
		response.BadRequest(c, apierr.InvalidParent, err.Error(), nil)
	case errors.Is(err, ErrCycleDetected):
		response.BadRequest(c, apierr.CycleDetected, err.Error(), nil)
	case errors.Is(err, ErrMaxDepth):
		response.BadRequest(c, apierr.MaxDepthExceeded, err.Error(), nil)
	case errors.Is(err, ErrSystemImmutable):
		response.BadRequest(c, apierr.SystemCategoryImmutable, err.Error(), nil)
	default:
		response.InternalError(c, "Category operation failed", err)
	}
}
