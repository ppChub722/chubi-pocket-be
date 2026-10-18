package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type GeneralResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   *APIError   `json:"error,omitempty"`
}

type APIError struct {
	Code    string      `json:"code,omitempty"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

func Success(c *gin.Context, status int, message string, data interface{}) {
	c.JSON(status, GeneralResponse{
		Success: true,
		Message: message,
		Data:    data,
	})
}

func Created(c *gin.Context, message string, data interface{}) {
	Success(c, http.StatusCreated, message, data)
}

func OK(c *gin.Context, message string, data interface{}) {
	Success(c, http.StatusOK, message, data)
}

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

func BadRequest(c *gin.Context, errCode, errMsg string, details interface{}) {
	Fail(c, http.StatusBadRequest, errCode, errMsg, details)
}

func NotFound(c *gin.Context, errCode, errMsg string) {
	Fail(c, http.StatusNotFound, errCode, errMsg, nil)
}

func InternalError(c *gin.Context, errMsg string, details interface{}) {
	Fail(c, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", errMsg, details)
}
