package logger

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// HeaderRequestID is the correlation header shared with the FE clients
// (dio/web HTTP clients generate one per outbound request — logging-plan.md
// §Frontend hookup). Already CORS-allowlisted + exposed in main.go.
const HeaderRequestID = "X-Request-ID"

const (
	ctxKeyRequestID = "request_id"

	// Must match auth's ctxKeyUserID (internal/modules/auth/middleware.go
	// c.Set("user_id", ...)). Duplicated as a string literal to avoid a
	// platform→modules import.
	ctxKeyUserID = "user_id"
)

// RequestIDMiddleware reads X-Request-ID from the client, validates it, and
// generates a UUID when absent or unusable. The ID is stored on the
// gin.Context and echoed back in the response header so testers can copy it
// from error screens into bug reports.
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

// validRequestID accepts client-supplied IDs only when sane: non-empty,
// ≤64 bytes, printable ASCII with no spaces/control chars. Anything else is
// regenerated — never trust a header enough to write it into logs verbatim.
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

// newRequestID prefers UUIDv7 (time-sortable — matches the PK style used
// elsewhere, e.g. auth's decoy user IDs); falls back to v4 on the
// (practically impossible) entropy error.
func newRequestID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

// RequestIDFromCtx returns the request ID set by RequestIDMiddleware, or ""
// when the middleware hasn't run.
func RequestIDFromCtx(c *gin.Context) string {
	return c.GetString(ctxKeyRequestID)
}

// FromCtx returns `base` pre-bound with request_id (and user_id when the
// auth middleware has populated it). Handlers/services use this so every
// line they emit correlates with the HTTP trace.
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
