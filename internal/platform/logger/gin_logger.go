package logger

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// maxBodyLog — logged body size cap (logging-plan.md: 4KB).
	maxBodyLog = 4 * 1024
	// maxBodyCapture — hard cap on how much we buffer for redaction.
	// Redaction needs the complete JSON document (truncating first would
	// break parsing), so bodies larger than this are skipped outright.
	maxBodyCapture = 64 * 1024

	bodyTooLarge = "[BODY TOO LARGE — SKIPPED]"
	truncMark    = "…[TRUNCATED]"
)

// GinLoggerMiddleware replaces the default Gin logger with slog.
// Every line carries request_id (set by RequestIDMiddleware — wire that
// FIRST), user_id (when authed), status, method, path, query, latency_ms,
// ip. Level by status: 5xx → Error, 4xx → Warn, else Info.
//
// When logBodies is true (LOG_BODIES=true, closed beta only) request and
// response bodies are captured too — JSON content types only, redacted via
// RedactJSON BEFORE truncation at 4KB. Multipart/binary is never captured.
func GinLoggerMiddleware(log *slog.Logger, logBodies bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		// --- Request body capture (gated) ---
		var reqBody []byte
		reqIsJSON := isJSONContent(c.ContentType())
		if logBodies && reqIsJSON && c.Request.Body != nil {
			raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyCapture+1))
			if err == nil {
				reqBody = raw
				// Restore the body for the handler (chain any remainder past
				// the capture cap so oversized requests still parse fully).
				c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), c.Request.Body))
			}
		}

		// --- Response body capture (gated) ---
		var resWriter *bodyCaptureWriter
		if logBodies {
			resWriter = &bodyCaptureWriter{ResponseWriter: c.Writer}
			c.Writer = resWriter
		}

		// Process Request
		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		attrs := []any{
			"request_id", RequestIDFromCtx(c),
			"status", status,
			"method", c.Request.Method,
			"path", path,
			"query", query,
			"latency_ms", float64(latency.Nanoseconds()) / 1e6,
			"ip", c.ClientIP(),
		}
		// user_id is set by the auth middleware (runs inside this one, so
		// it's present by the time c.Next() returns on protected routes).
		if uid, ok := c.Get(ctxKeyUserID); ok {
			attrs = append(attrs, "user_id", uid)
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, "error", c.Errors.String())
		}

		if logBodies {
			if reqIsJSON && len(reqBody) > 0 {
				attrs = append(attrs, "req_body", loggableBody(reqBody))
			}
			if resWriter != nil && resWriter.size > 0 &&
				isJSONContent(c.Writer.Header().Get("Content-Type")) {
				if resWriter.size > maxBodyCapture {
					attrs = append(attrs, "res_body", bodyTooLarge)
				} else {
					attrs = append(attrs, "res_body", loggableBody(resWriter.buf.Bytes()))
				}
			}
		}

		// Choose log level based on status code
		switch {
		case status >= 500:
			log.Error("HTTP Request", attrs...)
		case status >= 400:
			log.Warn("HTTP Request", attrs...)
		default:
			log.Info("HTTP Request", attrs...)
		}
	}
}

// loggableBody redacts FIRST (needs whole JSON), then truncates to 4KB.
// Bodies past the capture cap are skipped — redacting a partial document
// isn't safe.
func loggableBody(raw []byte) string {
	if len(raw) > maxBodyCapture {
		return bodyTooLarge
	}
	red := RedactJSON(raw)
	if len(red) > maxBodyLog {
		return string(red[:maxBodyLog]) + truncMark
	}
	return string(red)
}

// isJSONContent gates body capture to JSON payloads (application/json,
// application/*+json, ...). Multipart uploads and binary responses are
// skipped entirely.
func isJSONContent(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "json")
}

// bodyCaptureWriter tees the response into a buffer (capped at
// maxBodyCapture+1 so oversized bodies are detected and skipped, not
// half-logged).
type bodyCaptureWriter struct {
	gin.ResponseWriter
	buf  bytes.Buffer
	size int // total bytes written, even past the capture cap
}

func (w *bodyCaptureWriter) Write(b []byte) (int, error) {
	w.capture(b)
	return w.ResponseWriter.Write(b)
}

func (w *bodyCaptureWriter) WriteString(s string) (int, error) {
	w.capture([]byte(s))
	return w.ResponseWriter.WriteString(s)
}

func (w *bodyCaptureWriter) capture(b []byte) {
	w.size += len(b)
	if room := maxBodyCapture + 1 - w.buf.Len(); room > 0 {
		if len(b) > room {
			b = b[:room]
		}
		w.buf.Write(b)
	}
}
