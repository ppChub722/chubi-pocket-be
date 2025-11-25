package auth

import (
	"github.com/gin-gonic/gin"

	"github.com/ppChub722/finna-bbear-be/internal/platform/response"
)

type Handler struct {
	service *Service // Changed: Now talks to Service, not Store
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

	// 1. HTTP Validation
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", err.Error(), nil)
		return
	}

	// 2. Call Service (The Business Logic)
	token, user, err := h.service.RegisterUser(c.Request.Context(), req)
	if err != nil {
		if err == ErrDuplicateEntry {
			response.BadRequest(c, "DUPLICATE_USER", "Username or Email already registered", nil)
			return
		}
		response.InternalError(c, "Registration failed", err.Error())
		return
	}

	// 3. HTTP Response
	response.Created(c, "User registered successfully", gin.H{
		"token": token,
		"user":  user,
	})
}

// Login handles user authentication
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", err.Error(), nil)
		return
	}

	// Call Service
	token, user, err := h.service.LoginUser(c.Request.Context(), req)
	if err != nil {
		// We can check error types here to return 401 vs 500
		response.BadRequest(c, "AUTH_FAILED", "Invalid credentials", nil)
		return
	}

	response.OK(c, "Login successful", gin.H{
		"token": token,
		"user": gin.H{
			"id":       user.ID,
			"username": user.Username,
			"email":    user.Email,
		},
	})
}