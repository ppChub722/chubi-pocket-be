package users

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service     *Service
	authService *auth.Service
}

// NewHandler — authService injected so PUT /v1/users/me/password can alias
// /v1/auth/password (per phase0/be.md) without duplicating logic.
func NewHandler(s *Service, a *auth.Service) *Handler {
	return &Handler{service: s, authService: a}
}

// GET /v1/users/me
func (h *Handler) GetMe(c *gin.Context) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	prof, err := h.service.GetProfile(c.Request.Context(), id)
	if err != nil {
		response.InternalError(c, "Failed to fetch profile", err.Error())
		return
	}
	c.JSON(http.StatusOK, prof)
}

// PUT /v1/users/me
func (h *Handler) UpdateMe(c *gin.Context) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	prof, err := h.service.UpdateProfile(c.Request.Context(), id, req)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnknownPreferenceKey):
			response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		case errors.Is(err, ErrEmailExists):
			response.Fail(c, http.StatusConflict, "EMAIL_EXISTS",
				"Email already registered", nil)
		default:
			response.InternalError(c, "Failed to update profile", err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, prof)
}

// PUT /v1/users/me/password — alias to /v1/auth/password (phase0/be.md).
func (h *Handler) ChangePassword(c *gin.Context) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	var req auth.ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	if err := h.authService.ChangePassword(c.Request.Context(), id, req); err != nil {
		switch {
		case errors.Is(err, auth.ErrWrongPassword):
			response.Fail(c, http.StatusUnauthorized, "WRONG_PASSWORD", err.Error(), nil)
		case errors.Is(err, auth.ErrSamePassword):
			response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		default:
			response.InternalError(c, "Failed to change password", err.Error())
		}
		return
	}
	response.OK(c, "Password updated successfully", nil)
}

// POST /v1/users/me/deactivate
func (h *Handler) Deactivate(c *gin.Context) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	if err := h.service.Deactivate(c.Request.Context(), id); err != nil {
		response.InternalError(c, "Failed to deactivate", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Account deactivated", "status": "inactive"})
}

// POST /v1/users/me/reactivate
func (h *Handler) Reactivate(c *gin.Context) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	user, _ := auth.UserFromContext(c)

	if err := h.service.Reactivate(c.Request.Context(), id, user.Status); err != nil {
		if errors.Is(err, ErrAlreadyActive) {
			response.Fail(c, http.StatusBadRequest, "ALREADY_ACTIVE", "Account is already active", nil)
			return
		}
		response.InternalError(c, "Failed to reactivate", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Account reactivated", "status": "active"})
}
