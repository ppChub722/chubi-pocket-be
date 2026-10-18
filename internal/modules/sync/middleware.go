package sync

import (
	"bytes"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/logger"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// VersionsHeader carries the caller's fingerprints on every successful write.
const VersionsHeader = "X-Sync-Versions"

// skipRoutes write nothing the app caches: upload parsers, and /sync itself (it answers with versions).
var skipRoutes = map[string]bool{
	"/api/v1/pending-transactions/scan-slip":  true,
	"/api/v1/pending-transactions/parse-text": true,
	"/api/v1/slip-imports/check":              true,
	"/api/v1/sync":                            true,
}

// Middleware stamps X-Sync-Versions on every non-GET request that answers 2xx. It runs after the
// handler (the write has committed), so the fingerprints include it. gin sends the headers with the
// first body byte, so the response is held back and written for real once the header is set.
func Middleware(db *pgxpool.Pool, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if skipRoutes[c.FullPath()] {
			c.Next()
			return
		}
		orig := c.Writer
		held := &heldWriter{ResponseWriter: orig, status: http.StatusOK}
		c.Writer = held
		defer func() { c.Writer = orig }()
		c.Next()

		if held.status/100 == 2 {
			if userID, ok := auth.UserIDFromContext(c); ok {
				tz := c.GetHeader("X-Timezone")
				ctx := shared.WithHeaderTZ(c.Request.Context(), tz)
				if v, err := Fingerprints(ctx, db, userID, tz); err != nil {
					logger.FromCtx(c, log).Error("sync versions header", "error", err)
				} else {
					c.Header(VersionsHeader, formatVersions(v))
				}
			}
		}
		orig.WriteHeader(held.status)
		if held.body.Len() == 0 {
			orig.WriteHeaderNow()
			return
		}
		_, _ = orig.Write(held.body.Bytes())
	}
}

func formatVersions(v map[string]string) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + v[k]
	}
	return strings.Join(parts, ";")
}

// heldWriter keeps the status and body until the middleware releases them.
type heldWriter struct {
	gin.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *heldWriter) WriteHeader(code int)              { w.status = code }
func (w *heldWriter) WriteHeaderNow()                   {}
func (w *heldWriter) Status() int                       { return w.status }
func (w *heldWriter) Size() int                         { return w.body.Len() }
func (w *heldWriter) Written() bool                     { return w.body.Len() > 0 }
func (w *heldWriter) Flush()                            {}
func (w *heldWriter) Write(b []byte) (int, error)       { return w.body.Write(b) }
func (w *heldWriter) WriteString(s string) (int, error) { return w.body.WriteString(s) }
