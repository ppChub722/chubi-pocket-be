package logger

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// GinLoggerMiddleware replaces the default Gin logger with slog
func GinLoggerMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		// Process Request
		c.Next()

		// Calculate latency
		latency := time.Since(start)
		status := c.Writer.Status()

		// Choose log level based on status code
		if status >= 500 {
			logger.Error("HTTP Request",
				"status", status,
				"method", c.Request.Method,
				"path", path,
				"query", query,
				"latency", latency,
				"error", c.Errors.String(),
			)
		} else {
			logger.Info("HTTP Request",
				"status", status,
				"method", c.Request.Method,
				"path", path,
				"latency", latency,
			)
		}
	}
}