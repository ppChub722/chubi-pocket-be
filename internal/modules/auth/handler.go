package auth

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// POST /v1/auth/register
func (h *Handler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	resp, err := h.service.Register(c.Request.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, ErrUsernameExists):
			response.Fail(c, http.StatusConflict, "USERNAME_EXISTS", "Username already registered", nil)
		case errors.Is(err, ErrEmailExists):
			response.Fail(c, http.StatusConflict, "EMAIL_EXISTS", "Email already registered", nil)
		default:
			response.InternalError(c, "Registration failed", err.Error())
		}
		return
	}

	c.JSON(http.StatusCreated, resp)
}

// POST /v1/auth/login
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	resp, err := h.service.Login(c.Request.Context(), req)
	if err != nil {
		// Spec §3.8: never distinguish "user not found" vs "wrong password".
		response.Fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid credentials", nil)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// POST /v1/auth/logout — Phase 0 best-effort, client just discards.
func (h *Handler) Logout(c *gin.Context) {
	response.OK(c, "Logged out successfully", nil)
}

// PUT /v1/auth/password
func (h *Handler) ChangePassword(c *gin.Context) {
	userID, ok := UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	var req ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	if err := h.service.ChangePassword(c.Request.Context(), userID, req); err != nil {
		switch {
		case errors.Is(err, ErrWrongPassword):
			response.Fail(c, http.StatusUnauthorized, "WRONG_PASSWORD", err.Error(), nil)
		case errors.Is(err, ErrSamePassword):
			response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		default:
			response.InternalError(c, "Failed to change password", err.Error())
		}
		return
	}

	response.OK(c, "Password updated successfully", nil)
}
