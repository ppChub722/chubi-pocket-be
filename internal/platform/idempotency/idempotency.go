// Package idempotency makes a write safe to retry (0.4.2-3, owner 2026-10-30). A client sends one
// `Idempotency-Key` header per save and the SAME key on every retry. The first request with a key is
// run normally and, when it answers 2xx, its answer is stored under (user, key); a retry with the
// same key and the same request gets that stored answer (header `Idempotent-Replay: true`) and runs
// nothing, so a save that timed out on the client but landed on the server is not written twice.
//
// Rules: no header, a read, or a multipart upload = untouched. The same key for a different request
// (method / URI / body) = 422 IDEMPOTENCY_KEY_REUSED. A retry while the first is still running = 409
// IDEMPOTENCY_IN_PROGRESS (a first request that died without answering is taken over after
// abandonedAfter). A non-2xx answer is never stored, so a failed save can be retried for real. A
// storage error never blocks the request: it runs without the guarantee and is logged.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/logger"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

const (
	// Header is the request header the client sets.
	Header = "Idempotency-Key"
	// ReplayHeader is set on an answer that was stored by an earlier request.
	ReplayHeader = "Idempotent-Replay"

	maxKeyLen      = 200
	maxBody        = 8 << 20 // a JSON save; a bigger body runs without the guarantee
	maxStored      = 1 << 20
	retention      = "24 hours"
	abandonedAfter = time.Minute
	cleanEvery     = 200 // requests
)

var calls atomic.Uint64

// Middleware must run after auth (it keys on the caller).
func Middleware(db *pgxpool.Pool, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader(Header)
		if key == "" || !isWrite(c.Request.Method) || isMultipart(c) {
			c.Next()
			return
		}
		userID, ok := auth.UserIDFromContext(c)
		if !ok {
			c.Next()
			return
		}
		if len(key) > maxKeyLen {
			response.Validation(c, "Invalid Idempotency-Key", apierr.Field(Header, apierr.FieldInvalid))
			c.Abort()
			return
		}
		body, ok := readBody(c)
		if !ok {
			c.Next()
			return
		}
		sum := sha256.New()
		sum.Write([]byte(c.Request.Method + " " + c.Request.URL.RequestURI() + "\n"))
		sum.Write(body)
		hash := sum.Sum(nil)

		ctx := c.Request.Context()
		l := logger.FromCtx(c, log)
		uid := userID.String()
		go clean(db, l)

		inserted, err := claim(ctx, db, uid, key, hash)
		if err != nil {
			l.Error("idempotency claim", "error", err)
			c.Next()
			return
		}
		if !inserted {
			prev, err := lookup(ctx, db, uid, key)
			if err != nil {
				l.Error("idempotency lookup", "error", err)
				c.Next()
				return
			}
			switch {
			case prev == nil:
				// deleted between the two calls: run it for real
			case !bytes.Equal(prev.hash, hash):
				response.Fail(c, http.StatusUnprocessableEntity, apierr.IdempotencyKeyReused,
					"The Idempotency-Key was already used for a different request", nil)
				c.Abort()
				return
			case prev.status != nil:
				c.Header(ReplayHeader, "true")
				c.Data(int(*prev.status), prev.contentType, prev.body)
				c.Abort()
				return
			case time.Since(prev.createdAt) < abandonedAfter:
				response.Fail(c, http.StatusConflict, apierr.IdempotencyInProgress,
					"The first request with this Idempotency-Key is still running", nil)
				c.Abort()
				return
			default:
				// the first request died without answering: take it over
				if err := takeOver(ctx, db, uid, key); err != nil {
					l.Error("idempotency take over", "error", err)
				}
			}
		}

		orig := c.Writer
		cw := &captureWriter{ResponseWriter: orig}
		c.Writer = cw
		defer func() {
			c.Writer = orig
			// detached: the request context may be cancelled by now
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			status := cw.Status()
			if status/100 == 2 && cw.body.Len() <= maxStored {
				if err := save(sctx, db, uid, key, status, cw.Header().Get("Content-Type"), cw.body.Bytes()); err != nil {
					l.Error("idempotency save", "error", err)
				}
				return
			}
			if err := release(sctx, db, uid, key); err != nil {
				l.Error("idempotency release", "error", err)
			}
		}()
		c.Next()
	}
}

func isWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func isMultipart(c *gin.Context) bool {
	mt, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	return mt == "multipart/form-data"
}

// readBody returns the request body and puts it back for the handler. false = too big to hash: the
// request runs without the guarantee.
func readBody(c *gin.Context) ([]byte, bool) {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return nil, true
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), c.Request.Body))
		return nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return body, true
}

type captureWriter struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *captureWriter) Write(b []byte) (int, error) {
	if w.body.Len() <= maxStored {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	if w.body.Len() <= maxStored {
		w.body.WriteString(s)
	}
	return w.ResponseWriter.WriteString(s)
}

type storedRow struct {
	hash        []byte
	status      *int16
	contentType string
	body        []byte
	createdAt   time.Time
}

func claim(ctx context.Context, db *pgxpool.Pool, userID, key string, hash []byte) (bool, error) {
	tag, err := db.Exec(ctx, `
		INSERT INTO idempotency_keys (user_id, key, request_hash) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO NOTHING`, userID, key, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func lookup(ctx context.Context, db *pgxpool.Pool, userID, key string) (*storedRow, error) {
	var r storedRow
	var ct *string
	err := db.QueryRow(ctx, `
		SELECT request_hash, status_code, content_type, response_body, created_at
		FROM idempotency_keys WHERE user_id = $1 AND key = $2`, userID, key).
		Scan(&r.hash, &r.status, &ct, &r.body, &r.createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ct != nil {
		r.contentType = *ct
	}
	if r.contentType == "" {
		r.contentType = "application/json; charset=utf-8"
	}
	return &r, nil
}

func takeOver(ctx context.Context, db *pgxpool.Pool, userID, key string) error {
	_, err := db.Exec(ctx, `
		UPDATE idempotency_keys SET created_at = now()
		WHERE user_id = $1 AND key = $2 AND status_code IS NULL`, userID, key)
	return err
}

func save(ctx context.Context, db *pgxpool.Pool, userID, key string, status int, contentType string, body []byte) error {
	_, err := db.Exec(ctx, `
		UPDATE idempotency_keys SET status_code = $3, content_type = $4, response_body = $5
		WHERE user_id = $1 AND key = $2`, userID, key, int16(status), contentType, body)
	return err
}

// release drops a claim whose request did not answer 2xx, so the retry runs for real.
func release(ctx context.Context, db *pgxpool.Pool, userID, key string) error {
	_, err := db.Exec(ctx, `DELETE FROM idempotency_keys WHERE user_id = $1 AND key = $2 AND status_code IS NULL`, userID, key)
	return err
}

// clean deletes the keys past their retention, once every cleanEvery requests.
func clean(db *pgxpool.Pool, l *slog.Logger) {
	if calls.Add(1)%cleanEvery != 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < now() - $1::interval`, retention); err != nil {
		l.Error("idempotency clean", "error", err)
	}
}
