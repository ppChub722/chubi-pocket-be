package logger

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const HeaderRequestID = "X-Request-ID"

const (
	ctxKeyRequestID = "request_id"

	// Must match auth's ctxKeyUserID (internal/modules/auth/middleware.go c.Set("user_id", ...)).
	ctxKeyUserID = "user_id"
)

func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader(HeaderRequestID)
		if !validRequestID(rid) {
			rid = newRequestID()
		}
		c.Set(ctxKeyRequestID, rid)
		c.Writer.Header().Set(HeaderRequestID, rid)
		c.Next()
	}
}

func validRequestID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '!' || s[i] > '~' { // graphic ASCII only
			return false
		}
	}
	return true
}

func newRequestID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

func RequestIDFromCtx(c *gin.Context) string {
	return c.GetString(ctxKeyRequestID)
}

func FromCtx(c *gin.Context, base *slog.Logger) *slog.Logger {
	log := base
	if rid := RequestIDFromCtx(c); rid != "" {
		log = log.With("request_id", rid)
	}
	if uid, ok := c.Get(ctxKeyUserID); ok {
		log = log.With("user_id", uid)
	}
	return log
}
