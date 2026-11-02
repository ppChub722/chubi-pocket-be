package connections

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Handler struct {
	s          *Service
	tokenLimit *limiter // POST /auth/telegram/token, per IP: 20 per 10 minutes
}

func NewHandler(s *Service) *Handler {
	return &Handler{s: s, tokenLimit: newLimiter(20, 10*time.Minute)}
}

// LoginToken — POST /auth/telegram/token {id_token}: log in, or sign up at once (0.4.2-4).
func (h *Handler) LoginToken(c *gin.Context) {
	if !h.tokenLimit.allow(c.ClientIP()) {
		rateLimited(c)
		return
	}
	var req struct {
		IDToken string `json:"id_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IDToken == "" {
		response.Validation(c, "body must be {id_token}")
		return
	}
	out, err := h.s.TelegramTokenLogin(c.Request.Context(), req.IDToken)
	if err != nil {
		h.loginFail(c, err)
		return
	}
	status := http.StatusOK
	if out.Status == "signup" {
		status = http.StatusCreated
	}
	c.JSON(status, out)
}

// Link — POST /connections/:provider {id_token}: link the Telegram the id_token names (0.4.2-4).
func (h *Handler) Link(c *gin.Context) {
	var req struct {
		IDToken string `json:"id_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IDToken == "" {
		response.Validation(c, "body must be {id_token}")
		return
	}
	h.one(c, func(userID uuid.UUID, key string) (any, error) {
		return h.s.LinkTelegram(c.Request.Context(), userID, key, req.IDToken)
	})
}

func (h *Handler) loginFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrIDTokenInvalid):
		telegramTokenInvalid(c)
	case errors.Is(err, ErrAlreadyLinked):
		response.Fail(c, http.StatusConflict, apierr.TelegramAlreadyLinked, "This Telegram has an account: log in instead", nil)
	case errors.Is(err, ErrRegistrationClosed):
		response.Fail(c, http.StatusForbidden, apierr.RegistrationClosed, "Registration is closed", nil)
	case errors.Is(err, ErrUnavailable):
		response.Fail(c, http.StatusConflict, apierr.ConnectionUnavailable, "Telegram login is not available", nil)
	default:
		response.InternalError(c, "Telegram login failed", err)
	}
}

func telegramTokenInvalid(c *gin.Context) {
	response.Fail(c, http.StatusUnauthorized, apierr.TelegramTokenInvalid, "The Telegram login expired, try again", nil)
}

func rateLimited(c *gin.Context) {
	response.Fail(c, http.StatusTooManyRequests, apierr.RateLimited, "Too many tries, wait a few minutes", nil)
}

// limiter — a fixed window per key (in memory; one API process).
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	seen   map[string]*bucket
}

type bucket struct {
	start time.Time
	n     int
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, seen: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.seen) > 10000 { // drop old windows now and then
		for k, b := range l.seen {
			if now.Sub(b.start) > l.window {
				delete(l.seen, k)
			}
		}
	}
	b := l.seen[key]
	if b == nil || now.Sub(b.start) > l.window {
		b = &bucket{start: now}
		l.seen[key] = b
	}
	b.n++
	return b.n <= l.max
}

func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	out, err := h.s.List(c.Request.Context(), userID)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Get(c *gin.Context) {
	h.one(c, func(userID uuid.UUID, key string) (any, error) { return h.s.Get(c.Request.Context(), userID, key) })
}

func (h *Handler) Unlink(c *gin.Context) {
	h.one(c, func(userID uuid.UUID, key string) (any, error) { return h.s.Unlink(c.Request.Context(), userID, key) })
}

func (h *Handler) ImportPhoto(c *gin.Context) {
	h.one(c, func(userID uuid.UUID, key string) (any, error) {
		return h.s.ImportPhoto(c.Request.Context(), userID, key)
	})
}

func (h *Handler) UpdatePermissions(c *gin.Context) {
	var patch map[string]bool
	if err := c.ShouldBindJSON(&patch); err != nil {
		response.Validation(c, "body must be an object of {key: bool}")
		return
	}
	h.one(c, func(userID uuid.UUID, key string) (any, error) {
		return h.s.UpdatePermissions(c.Request.Context(), userID, key, patch)
	})
}

func (h *Handler) one(c *gin.Context, fn func(userID uuid.UUID, key string) (any, error)) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	out, err := fn(userID, c.Param("provider"))
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrUnknownProvider):
		response.NotFound(c, apierr.NotFound, "Unknown provider")
	case errors.Is(err, ErrBadPermissions):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrIDTokenInvalid):
		telegramTokenInvalid(c)
	case errors.Is(err, ErrUnavailable):
		response.Fail(c, http.StatusConflict, apierr.ConnectionUnavailable, "This app can't be linked yet", nil)
	case errors.Is(err, ErrNotLinked):
		response.Fail(c, http.StatusConflict, apierr.NotLinked, "This app is not linked", nil)
	case errors.Is(err, ErrPermissionUnavailable):
		response.Fail(c, http.StatusConflict, apierr.PermissionUnavailable, err.Error(), nil)
	case errors.Is(err, ErrNoPhoto):
		response.NotFound(c, apierr.NoPhoto, "The app gave no picture")
	case errors.Is(err, shared.ErrLastLoginMethod):
		response.Fail(c, http.StatusConflict, apierr.LastLoginMethod,
			"This is the only way into an account without a password: set a password first", nil)
	case errors.Is(err, ErrProvider):
		// the cause may name the app's URL; it is logged by the request log only as status 502
		response.Fail(c, http.StatusBadGateway, apierr.ProviderError, "The app did not answer, try again", nil)
	default:
		response.InternalError(c, "Connections failed", err)
	}
}

// Image — GET /avatars/:id: a stored picture (Bearer auth; any signed-in user, like icons).
func (h *Handler) Image(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.NotFound(c, apierr.NotFound, "No such picture")
		return
	}
	img, ok, err := h.s.Image(c.Request.Context(), id)
	if err != nil {
		response.InternalError(c, "Failed to read the picture", err)
		return
	}
	if !ok {
		response.NotFound(c, apierr.NotFound, "No such picture")
		return
	}
	c.Header("Cache-Control", "private, max-age=31536000, immutable")
	c.Data(http.StatusOK, "image/jpeg", img)
}

// BrandLogo — GET /brand-logos/:file (<provider>.png): public, embedded in the binary, no service.
func BrandLogo(c *gin.Context) {
	file := c.Param("file")
	key := strings.TrimSuffix(file, ".png")
	if _, ok := providerOf(key); !ok || key == file {
		response.NotFound(c, apierr.NotFound, "No such logo")
		return
	}
	b, err := logos.ReadFile("logos/" + key + ".png")
	if err != nil {
		response.NotFound(c, apierr.NotFound, "No such logo")
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Data(http.StatusOK, "image/png", b)
}
