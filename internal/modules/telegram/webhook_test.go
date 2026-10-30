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
	w := NewWebhook(testSecret, NewHandler(s, nil, newAccounts(), log), log)
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
	if len(s.sent) != 1 || s.sent[0].text != TextReceived || s.sent[0].chatID != linkedChat || s.sent[0].replyTo != 3 {
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
	w := New(config.TelegramConfig{BotToken: testToken, WebhookSecret: testSecret}, nil, log)
	if w == nil || w.Client() == nil {
		t.Errorf("good config: %+v", w)
	}
	acc := newAccounts()
	w.WithAccounts(acc)
	if w.h.accounts != acc {
		t.Errorf("accounts not wired")
	}
}

func TestUsernameIsCached(t *testing.T) {
	srv, calls := fakeBotAPI(t, 200, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"chubiPocket_bot"}}`)
	c := NewClient(testToken, srv.URL, srv.Client())
	for i := 0; i < 2; i++ {
		if u, err := c.Username(context.Background()); err != nil || u != "chubiPocket_bot" {
			t.Fatalf("username = %q, %v", u, err)
		}
	}
	if len(*calls) != 1 {
		t.Errorf("getMe calls = %d, want 1 (cached)", len(*calls))
	}
}

func TestProfilePhoto(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUserProfilePhotos"):
			if !strings.Contains(string(raw), `"user_id":1001`) || !strings.Contains(string(raw), `"limit":1`) {
				t.Errorf("getUserProfilePhotos body = %s", raw)
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":{"total_count":1,"photos":[[
				{"file_id":"small","width":160,"height":160},{"file_id":"big","width":640,"height":640}]]}}`)
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			if !strings.Contains(string(raw), `"file_id":"small"`) {
				t.Errorf("getFile body = %s", raw)
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_id":"small","file_path":"photos/file_1.jpg"}}`)
		case r.URL.Path == "/file/bot"+testToken+"/photos/file_1.jpg":
			_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	img, err := NewClient(testToken, srv.URL, srv.Client()).ProfilePhoto(context.Background(), 1001)
	if err != nil || len(img) != 4 || img[0] != 0xFF {
		t.Fatalf("photo = %v, %v (calls %v)", img, err, paths)
	}

	// no photo → nil, nil
	srv2, _ := fakeBotAPI(t, 200, `{"ok":true,"result":{"total_count":0,"photos":[]}}`)
	if img, err := NewClient(testToken, srv2.URL, srv2.Client()).ProfilePhoto(context.Background(), 1); img != nil || err != nil {
		t.Errorf("no photo = %v, %v", img, err)
	}

	// a failed download never carries the token
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getUserProfilePhotos") {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"photos":[[{"file_id":"a","width":160,"height":160}]]}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_path":"p.jpg"}}`)
			return
		}
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv3.Close()
	_, err = NewClient(testToken, srv3.URL, srv3.Client()).ProfilePhoto(context.Background(), 1)
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("download error = %v", err)
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

	// user_id (migration 121): a missing user would break the FK, so NULL here; the link path is
	// covered by the connections DB test
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
