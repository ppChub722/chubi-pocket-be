package auth

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/logger"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
	log     *slog.Logger
}

func NewHandler(s *Service, log *slog.Logger) *Handler {
	return &Handler{service: s, log: log}
}

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

func (h *Handler) Login(c *gin.Context) {
	// 0.4.2 1b-2 (owner): every login failure is one answer, a bad / empty body included.
	var req LoginRequest
	err := c.ShouldBindJSON(&req)
	var resp *AuthResponse
	if err == nil {
		resp, err = h.service.Login(c.Request.Context(), req)
	}
	if err != nil {
		// Never log the identifier verbatim (could be an email — PII).
		logger.FromCtx(c, h.log).Warn("auth login failed")
		response.Fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid credentials", nil)
		return
	}

	logger.FromCtx(c, h.log).Info("auth login success", "user_id", resp.User.ID)
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) Logout(c *gin.Context) {
	logger.FromCtx(c, h.log).Info("auth logout")
	response.OK(c, "Logged out successfully", nil)
}

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
			logger.FromCtx(c, h.log).Warn("auth password change rejected", "reason", "wrong_current_password")
			response.Fail(c, http.StatusUnauthorized, "WRONG_PASSWORD", err.Error(), nil)
		case errors.Is(err, ErrSamePassword):
			logger.FromCtx(c, h.log).Warn("auth password change rejected", "reason", "same_password")
			response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		default:
			logger.FromCtx(c, h.log).Error("auth password change failed", "error", err)
			response.InternalError(c, "Failed to change password", err.Error())
		}
		return
	}

	logger.FromCtx(c, h.log).Info("auth password changed")
	response.OK(c, "Password updated successfully", nil)
}
