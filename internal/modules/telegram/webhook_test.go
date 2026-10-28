package telegram

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/logger"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

const testSecret = "hook_SECRET-1"

func newTestRouter(t *testing.T) (*gin.Engine, *fakeSender, *bytes.Buffer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	s := &fakeSender{}
	w := NewWebhook(testSecret, NewHandler(s, nil, []int64{allowedChat}, log), log)
	r := gin.New()
	r.Use(logger.GinLoggerMiddleware(log, true, Path)) // bodies on, as on the closed-beta VPS
	r.POST(Path, w.Serve)
	return r, s, &logs
}

func post(r *gin.Engine, secret, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set(SecretHeader, secret)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const goodUpdate = `{"update_id":900,"message":{"message_id":3,"date":1761631200,
	"chat":{"id":1001,"type":"private"},"from":{"id":5,"username":"nong"},"text":"ข้าวมันไก่ 50"}}`

func TestWebhookSecret(t *testing.T) {
	for name, secret := range map[string]string{"missing": "", "wrong": "nope", "prefix": "hook_SECRET"} {
		r, s, _ := newTestRouter(t)
		if rec := post(r, secret, goodUpdate); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s secret: %d, want 401", name, rec.Code)
		}
		if len(s.sent) != 0 {
			t.Errorf("%s secret: replied", name)
		}
	}
}

func TestWebhookGoodUpdate(t *testing.T) {
	r, s, logs := newTestRouter(t)
	rec := post(r, testSecret, goodUpdate)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body %q, want empty (Telegram would run a method from it)", rec.Body.String())
	}
	if len(s.sent) != 1 || s.sent[0].text != TextReceived || s.sent[0].chatID != allowedChat || s.sent[0].replyTo != 3 {
		t.Errorf("sent = %+v", s.sent)
	}
	if strings.Contains(logs.String(), "ข้าวมันไก่") {
		t.Errorf("the request log holds the message text: %s", logs.String())
	}
	// Telegram resends it → 200, no second reply
	if rec := post(r, testSecret, goodUpdate); rec.Code != http.StatusOK || len(s.sent) != 1 {
		t.Errorf("resend: %d, replies %d", rec.Code, len(s.sent))
	}
}

func TestWebhookBadJSON(t *testing.T) {
	for _, body := range []string{`{not json`, ``, `[]`} {
		r, s, _ := newTestRouter(t)
		if rec := post(r, testSecret, body); rec.Code != http.StatusOK {
			t.Errorf("%q: status %d, want 200", body, rec.Code)
		}
		if len(s.sent) != 0 {
			t.Errorf("%q: replied", body)
		}
	}
}

func TestNewIsOffWithoutTokenOrGoodSecret(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	if New(config.TelegramConfig{}, nil, log) != nil {
		t.Errorf("no token: want off")
	}
	for _, secret := range []string{"", "has space", "bad!char", strings.Repeat("a", 257)} {
		if New(config.TelegramConfig{BotToken: testToken, WebhookSecret: secret}, nil, log) != nil {
			t.Errorf("secret %q: want off", secret)
		}
	}
	if strings.Contains(logs.String(), testToken) {
		t.Errorf("token in the log")
	}
	w := New(config.TelegramConfig{BotToken: testToken, WebhookSecret: testSecret, AllowedChatIDs: "1,2"}, nil, log)
	if w == nil || w.allowedN != 2 || w.client == nil {
		t.Errorf("good config: %+v", w)
	}
}

func TestStartSetsTheWebhook(t *testing.T) {
	var paths []string
	done := make(chan struct{}, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		_, _ = io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"chubi_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"url":"https://x.dev/w"}}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		}
		done <- struct{}{}
	}))
	defer srv.Close()

	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	w := NewWebhook(testSecret, nil, log)
	w.client, w.webhookURL = NewClient(testToken, srv.URL, srv.Client()), "https://x.dev/w"
	w.Start()
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Start made %d calls: %v", i, paths)
		}
	}
	if strings.Join(paths, ",") != "getMe,setWebhook,getWebhookInfo" {
		t.Errorf("calls = %v", paths)
	}
}

func TestStoreSave(t *testing.T) {
	pool := testdb.Pool(t)
	st := NewStore(pool)
	ctx := context.Background()
	id := rand.Int64N(1<<62) + 1<<40
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM telegram_messages WHERE update_id = $1`, id) })

	r := Received{UpdateID: id, ChatID: -1001, FromUsername: "", Text: "ข้าว 60",
		ReceivedAt: time.Unix(1761631200, 0).UTC()}
	kept, err := st.Save(ctx, r)
	if err != nil || !kept {
		t.Fatalf("first save = %v, %v", kept, err)
	}
	if kept, err := st.Save(ctx, r); err != nil || kept {
		t.Errorf("second save = %v, %v (want not kept)", kept, err)
	}
	var chat int64
	var user *string
	var text string
	var at time.Time
	if err := pool.QueryRow(ctx, `SELECT chat_id, from_username, text, received_at FROM telegram_messages
		WHERE update_id = $1`, id).Scan(&chat, &user, &text, &at); err != nil {
		t.Fatal(err)
	}
	if chat != -1001 || user != nil || text != "ข้าว 60" || !at.Equal(r.ReceivedAt) {
		t.Errorf("row = %d %v %q %v", chat, user, text, at)
	}
}
