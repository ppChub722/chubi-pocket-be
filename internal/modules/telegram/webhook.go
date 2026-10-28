package telegram

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

const (
	// Path is the webhook route (public: no JWT, no build check, no sync middleware).
	Path = "/api/v1/telegram/webhook"
	// SecretHeader carries TELEGRAM_WEBHOOK_SECRET on every Telegram call.
	SecretHeader = "X-Telegram-Bot-Api-Secret-Token"

	maxUpdateBytes = 1 << 20
	handleTimeout  = 8 * time.Second
)

// Telegram's rule for secret_token.
var validSecret = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// Webhook serves Telegram's calls. A wrong / missing secret → 401; anything else → 200, even a
// body that isn't an update, so Telegram never retries.
type Webhook struct {
	secret []byte
	h      *Handler
	log    *slog.Logger

	client     *Client // set by New; used by Start
	webhookURL string
	allowedN   int
}

func NewWebhook(secret string, h *Handler, log *slog.Logger) *Webhook {
	return &Webhook{secret: []byte(secret), h: h, log: log}
}

func (w *Webhook) Serve(c *gin.Context) {
	if subtle.ConstantTimeCompare([]byte(c.GetHeader(SecretHeader)), w.secret) != 1 {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Bad webhook secret", nil)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxUpdateBytes))
	var u Update
	if err == nil {
		err = json.Unmarshal(raw, &u)
	}
	if err != nil {
		w.log.Warn("telegram: unreadable update", "bytes", len(raw))
		c.Status(http.StatusOK)
		return
	}
	// the reply goes out before Telegram gets its 200; a closed connection doesn't cut it short
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), handleTimeout)
	defer cancel()
	w.h.HandleUpdate(ctx, u)
	c.Status(http.StatusOK)
}

// New wires the bot from TELEGRAM_*. nil = off: no token (local / tests), or a secret Telegram
// would reject (the route is then not registered; an error is logged).
func New(cfg config.TelegramConfig, db *pgxpool.Pool, log *slog.Logger) *Webhook {
	if cfg.BotToken == "" {
		return nil
	}
	if !validSecret.MatchString(cfg.WebhookSecret) {
		log.Error("telegram: off — TELEGRAM_WEBHOOK_SECRET must be 1-256 chars of A-Z a-z 0-9 _ -")
		return nil
	}
	client := NewClient(cfg.BotToken, DefaultAPIBase, nil)
	allowed := ParseChatIDs(cfg.AllowedChatIDs, log)
	w := NewWebhook(cfg.WebhookSecret, NewHandler(client, NewStore(db), allowed, log), log)
	w.client, w.webhookURL, w.allowedN = client, cfg.WebhookURL, len(allowed)
	return w
}

// Start checks the token (getMe) and points Telegram at TELEGRAM_WEBHOOK_URL, in the background:
// a bad token or Telegram being down is logged, the API runs on.
func (w *Webhook) Start() {
	if w.client == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		me, err := w.client.GetMe(ctx)
		if err != nil {
			w.log.Error("telegram: getMe failed — check TELEGRAM_BOT_TOKEN", "error", err)
			return
		}
		w.log.Info("telegram: on", "mode", "webhook", "bot", "@"+me.Username, "allowed_chats", w.allowedN)
		if w.webhookURL == "" {
			w.log.Warn("telegram: TELEGRAM_WEBHOOK_URL is empty — setWebhook skipped, Telegram keeps its last URL")
		} else if err := w.client.SetWebhook(ctx, w.webhookURL, string(w.secret)); err != nil {
			w.log.Error("telegram: setWebhook failed", "url", w.webhookURL, "error", err)
			return
		}
		info, err := w.client.GetWebhookInfo(ctx)
		if err != nil {
			w.log.Warn("telegram: getWebhookInfo failed", "error", err)
			return
		}
		w.log.Info("telegram: webhook", "url", info.URL, "pending", info.PendingUpdateCount,
			"last_error", info.LastErrorMessage)
	}()
}

// ParseChatIDs reads TELEGRAM_ALLOWED_CHAT_IDS ("123, -456"); a bad entry is logged and skipped.
func ParseChatIDs(raw string, log *slog.Logger) []int64 {
	var ids []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			log.Warn("telegram: skipping a bad TELEGRAM_ALLOWED_CHAT_IDS entry", "entry", part)
			continue
		}
		ids = append(ids, id)
	}
	return ids
}
