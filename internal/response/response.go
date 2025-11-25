package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GeneralResponse represents the basic structure of all API responses.
type GeneralResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   *APIError   `json:"error,omitempty"`
}

// APIError represents a structured error message.
type APIError struct {
	Code    string      `json:"code,omitempty"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

// Success sends a standard success response.
func Success(c *gin.Context, status int, message string, data interface{}) {
	c.JSON(status, GeneralResponse{
		Success: true,
		Message: message,
		Data:    data,
	})
}

// Created sends a 201 Created response.
func Created(c *gin.Context, message string, data interface{}) {
	Success(c, http.StatusCreated, message, data)
}

// OK sends a 200 OK response.
func OK(c *gin.Context, message string, data interface{}) {
	Success(c, http.StatusOK, message, data)
}

// Fail sends a standard error response.
func Fail(c *gin.Context, status int, errCode, errMsg string, details interface{}) {
	c.JSON(status, GeneralResponse{
		Success: false,
		Error: &APIError{
			Code:    errCode,
			Message: errMsg,
			Details: details,
		},
	})
}

// BadRequest sends a 400 Bad Request error response.
func BadRequest(c *gin.Context, errCode, errMsg string, details interface{}) {
	Fail(c, http.StatusBadRequest, errCode, errMsg, details)
}

// NotFound sends a 404 Not Found error response.
func NotFound(c *gin.Context, errCode, errMsg string) {
	Fail(c, http.StatusNotFound, errCode, errMsg, nil)
}

// InternalError sends a 500 Internal Server Error response.
func InternalError(c *gin.Context, errMsg string, details interface{}) {
	Fail(c, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", errMsg, details)
}
