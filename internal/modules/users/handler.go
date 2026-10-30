package users

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
	service     *Service
	authService *auth.Service
}

func NewHandler(s *Service, a *auth.Service) *Handler {
	return &Handler{service: s, authService: a}
}

func (h *Handler) GetMe(c *gin.Context) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	prof, err := h.service.GetProfile(c.Request.Context(), id)
	if err != nil {
		response.InternalError(c, "Failed to fetch profile", err)
		return
	}
	c.JSON(http.StatusOK, prof)
}

func (h *Handler) UpdateMe(c *gin.Context) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}

	prof, err := h.service.UpdateProfile(c.Request.Context(), id, req)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnknownPreferenceKey), errors.Is(err, ErrInvalidFeeCategory), errors.Is(err, ErrInvalidDefaultAccount),
			errors.Is(err, ErrInvalidAvatarSettings):
			response.BindError(c, err)
		case errors.Is(err, ErrAvatarSourceUnavailable):
			response.Fail(c, http.StatusConflict, apierr.AvatarSourceUnavailable,
				"No picture from that source (or the app is no longer linked)", nil)
		case errors.Is(err, ErrEmailExists):
			response.Fail(c, http.StatusConflict, apierr.EmailExists,
				"Email already registered", nil)
		default:
			response.InternalError(c, "Failed to update profile", err)
		}
		return
	}
	c.JSON(http.StatusOK, prof)
}

// DeleteAvatar — DELETE /avatars/:id where id is the source name (0.4.2 1b; one spec entry with
// GET /avatars/{id}).
func (h *Handler) DeleteAvatar(c *gin.Context) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	prof, err := h.service.DeleteAvatar(c.Request.Context(), id, c.Param("id"))
	if err != nil {
		if errors.Is(err, ErrInvalidAvatarSettings) {
			response.ValidationErr(c, err)
			return
		}
		response.InternalError(c, "Failed to delete the picture", err)
		return
	}
	c.JSON(http.StatusOK, prof)
}

func (h *Handler) Deactivate(c *gin.Context) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	if err := h.service.Deactivate(c.Request.Context(), id); err != nil {
		response.InternalError(c, "Failed to deactivate", err)
		return
	}
	h.respondProfile(c, id)
}

func (h *Handler) Reactivate(c *gin.Context) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	prof, err := h.service.GetProfile(c.Request.Context(), id)
	if err != nil {
		response.InternalError(c, "Failed to reactivate", err)
		return
	}

	if err := h.service.Reactivate(c.Request.Context(), id, prof.Status); err != nil {
		if errors.Is(err, ErrAlreadyActive) {
			response.Fail(c, http.StatusBadRequest, apierr.AlreadyActive, "Account is already active", nil)
			return
		}
		response.InternalError(c, "Failed to reactivate", err)
		return
	}
	h.respondProfile(c, id)
}

func (h *Handler) respondProfile(c *gin.Context, id uuid.UUID) {
	prof, err := h.service.GetProfile(c.Request.Context(), id)
	if err != nil {
		response.InternalError(c, "Failed to fetch profile", err)
		return
	}
	c.JSON(http.StatusOK, prof)
}
