package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/finna-bbear-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

// NewHandler initializes the HTTP handler with the Service
func NewHandler(s *Service) *Handler {
	return &Handler{
		service: s,
	}
}

// Register handles user signup
func (h *Handler) Register(c *gin.Context) {
	var req RegisterRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	token, user, err := h.service.RegisterUser(c.Request.Context(), req)
	if err != nil {
		if err == ErrDuplicateEntry {
			response.Fail(c, http.StatusConflict, "EMAIL_EXISTS", "Username or email already registered", nil)
			return
		}
		response.InternalError(c, "Registration failed", err.Error())
		return
	}

	response.Created(c, "User registered successfully", gin.H{
		"user": user,
		"token": gin.H{
			"access_token": token,
			"expires_in":   3600,
		},
	})
}

// Login handles user authentication
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	token, user, err := h.service.LoginUser(c.Request.Context(), req)
	if err != nil {
		response.Fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid credentials", nil)
		return
	}

	response.OK(c, "Login successful", gin.H{
		"user": gin.H{
			"id":       user.ID,
			"username": user.Username,
			"email":    user.Email,
		},
		"token": gin.H{
			"access_token": token,
			"expires_in":   3600,
		},
	})
}

// Logout handles token invalidation
func (h *Handler) Logout(c *gin.Context) {
	// For stateless JWT, logout is handled client-side by discarding the token.
	// If token blacklisting is needed later, it can be added here.
	response.OK(c, "Logged out successfully", nil)
}

// GetProfile handles GET /users/me
func (h *Handler) GetProfile(c *gin.Context) {
	userID := getUserID(c)
	if userID == 0 {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	user, err := h.service.GetProfile(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to fetch profile", err.Error())
		return
	}

	response.OK(c, "Profile fetched successfully", user)
}

// UpdateProfile handles PUT /users/me
func (h *Handler) UpdateProfile(c *gin.Context) {
	userID := getUserID(c)
	if userID == 0 {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	user, err := h.service.UpdateProfile(c.Request.Context(), userID, req)
	if err != nil {
		response.InternalError(c, "Failed to update profile", err.Error())
		return
	}

	response.OK(c, "Profile updated successfully", user)
}

// ChangePassword handles PUT /users/me/password
func (h *Handler) ChangePassword(c *gin.Context) {
	userID := getUserID(c)
	if userID == 0 {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	var req ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	err := h.service.ChangePassword(c.Request.Context(), userID, req)
	if err != nil {
		if err.Error() == "current password is incorrect" {
			response.Fail(c, http.StatusUnauthorized, "WRONG_PASSWORD", err.Error(), nil)
			return
		}
		response.InternalError(c, "Failed to change password", err.Error())
		return
	}

	response.OK(c, "Password updated successfully", nil)
}

// Helper to get UserID from context (set by Auth Middleware)
func getUserID(c *gin.Context) int64 {
	id, exists := c.Get("userID")
	if !exists {
		return 0
	}
	return id.(int64)
}
