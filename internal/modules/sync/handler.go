package sync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	gosync "sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/logger"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var datasetTimeout = 10 * time.Second

type Handler struct {
	db       *pgxpool.Pool
	log      *slog.Logger
	datasets map[string]dataset
}

func NewHandler(db *pgxpool.Pool, log *slog.Logger, s Services) *Handler {
	return &Handler{db: db, log: log, datasets: datasets(s)}
}

type Request struct {
	Want []string          `json:"want"`
	Have map[string]string `json:"have"`
}

type DatasetData struct {
	V    string `json:"v"`
	Body any    `json:"body"`
}

type DatasetError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	SyncedAt  time.Time               `json:"synced_at"`
	Versions  map[string]string       `json:"versions"`
	Data      map[string]DatasetData  `json:"data"`
	Unchanged []string                `json:"unchanged"`
	Errors    map[string]DatasetError `json:"errors"`
}

type loaded struct {
	body any
	err  error
}

// Sync — POST /sync: the datasets whose fingerprint differs from the caller's `have`, each whole.
func (h *Handler) Sync(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	var req Request
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid request body", nil)
		return
	}
	want, bad := h.resolveWant(req.Want)
	if len(bad) > 0 {
		response.BadRequest(c, "VALIDATION_ERROR", "Unknown or duplicate dataset in want", bad)
		return
	}

	tz := c.GetHeader("X-Timezone")
	ctx := shared.WithHeaderTZ(c.Request.Context(), tz)
	versions, err := Fingerprints(ctx, h.db, userID, tz)
	if err != nil {
		response.InternalError(c, "Failed to compute sync versions", err.Error())
		return
	}

	out := Response{
		SyncedAt:  time.Now().UTC(),
		Data:      map[string]DatasetData{},
		Unchanged: []string{},
		Errors:    map[string]DatasetError{},
	}
	var load []string
	for _, name := range want {
		if req.Have[name] == versions[name] {
			out.Unchanged = append(out.Unchanged, name)
			continue
		}
		load = append(load, name)
	}

	results := h.loadAll(ctx, userID, tz, load)
	mutated := false
	for _, name := range load {
		r := results[name]
		if r.err != nil {
			logger.FromCtx(c, h.log).Error("sync dataset failed", "dataset", name, "error", r.err)
			msg := "Failed to load " + name
			if errors.Is(r.err, context.DeadlineExceeded) {
				msg = "Timed out loading " + name
			}
			out.Errors[name] = DatasetError{Code: "INTERNAL_ERROR", Message: msg}
			continue
		}
		out.Data[name] = DatasetData{V: versions[name], Body: r.body}
		mutated = mutated || h.datasets[name].readsMutate
	}
	if mutated {
		after, err := Fingerprints(ctx, h.db, userID, tz)
		if err != nil {
			response.InternalError(c, "Failed to compute sync versions", err.Error())
			return
		}
		for name, d := range out.Data {
			if h.datasets[name].readsMutate {
				d.V = after[name]
				out.Data[name] = d
			}
		}
		versions = after
	}
	out.Versions = versions
	c.JSON(http.StatusOK, out)
}

// resolveWant: no names = every dataset. Returns the names it could not accept.
func (h *Handler) resolveWant(want []string) (names, bad []string) {
	if len(want) == 0 {
		return slices.Clone(DatasetNames), nil
	}
	seen := map[string]bool{}
	for _, n := range want {
		if _, known := h.datasets[n]; !known || seen[n] {
			bad = append(bad, n)
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	return names, bad
}

// loadAll runs the loaders in parallel, each with its own timeout; one failing does not stop the rest.
func (h *Handler) loadAll(ctx context.Context, userID uuid.UUID, tz string, names []string) map[string]loaded {
	var (
		mu  gosync.Mutex
		wg  gosync.WaitGroup
		out = make(map[string]loaded, len(names))
	)
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dctx, cancel := context.WithTimeout(ctx, datasetTimeout)
			defer cancel()
			body, err := h.datasets[name].load(dctx, userID, tz)
			mu.Lock()
			out[name] = loaded{body: body, err: err}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}
