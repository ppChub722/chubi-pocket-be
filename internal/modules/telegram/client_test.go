package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "123456:SECRET-token_abc"

type call struct {
	path string
	body map[string]any
}

// fakeBotAPI answers every method with result; calls are recorded.
func fakeBotAPI(t *testing.T, status int, reply string) (*httptest.Server, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		calls = append(calls, call{path: r.URL.Path, body: body})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestClientSendMessage(t *testing.T) {
	srv, calls := fakeBotAPI(t, 200, `{"ok":true,"result":{"message_id":9}}`)
	c := NewClient(testToken, srv.URL, srv.Client())
	if err := c.SendMessage(context.Background(), 42, "สวัสดี", 7); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d", len(*calls))
	}
	got := (*calls)[0]
	if got.path != "/bot"+testToken+"/sendMessage" {
		t.Errorf("path = %s", got.path)
	}
	if got.body["chat_id"] != float64(42) || got.body["text"] != "สวัสดี" {
		t.Errorf("body = %v", got.body)
	}
	rp, _ := got.body["reply_parameters"].(map[string]any)
	if rp["message_id"] != float64(7) || rp["allow_sending_without_reply"] != true {
		t.Errorf("reply_parameters = %v", got.body["reply_parameters"])
	}

	// no replyTo → no reply_parameters
	if err := c.SendMessage(context.Background(), 42, "x", 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := (*calls)[1].body["reply_parameters"]; ok {
		t.Errorf("reply_parameters sent without replyTo")
	}
}

func TestClientSetWebhookGetMeInfo(t *testing.T) {
	srv, calls := fakeBotAPI(t, 200, `{"ok":true,"result":true}`)
	c := NewClient(testToken, srv.URL, srv.Client())
	if err := c.SetWebhook(context.Background(), "https://x.dev/api/v1/telegram/webhook", "s3cret"); err != nil {
		t.Fatal(err)
	}
	b := (*calls)[0].body
	if (*calls)[0].path != "/bot"+testToken+"/setWebhook" || b["url"] != "https://x.dev/api/v1/telegram/webhook" ||
		b["secret_token"] != "s3cret" {
		t.Errorf("setWebhook = %s %v", (*calls)[0].path, b)
	}
	if au, _ := b["allowed_updates"].([]any); len(au) != 2 || au[0] != "message" || au[1] != "callback_query" {
		t.Errorf("allowed_updates = %v", b["allowed_updates"])
	}

	srv2, calls2 := fakeBotAPI(t, 200, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"chubi_bot","first_name":"Chubi"}}`)
	me, err := NewClient(testToken, srv2.URL, srv2.Client()).GetMe(context.Background())
	if err != nil || me.Username != "chubi_bot" || !me.IsBot {
		t.Errorf("getMe = %+v, %v", me, err)
	}
	if (*calls2)[0].path != "/bot"+testToken+"/getMe" {
		t.Errorf("getMe path = %s", (*calls2)[0].path)
	}

	srv3, _ := fakeBotAPI(t, 200, `{"ok":true,"result":{"url":"https://x.dev/w","pending_update_count":2,"last_error_message":"boom"}}`)
	info, err := NewClient(testToken, srv3.URL, srv3.Client()).GetWebhookInfo(context.Background())
	if err != nil || info.URL != "https://x.dev/w" || info.PendingUpdateCount != 2 || info.LastErrorMessage != "boom" {
		t.Errorf("getWebhookInfo = %+v, %v", info, err)
	}
}

func TestClientErrorsNeverCarryTheToken(t *testing.T) {
	// Bot API error (bad token): description kept, token not in the message
	srv, _ := fakeBotAPI(t, 401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	err := NewClient(testToken, srv.URL, srv.Client()).SendMessage(context.Background(), 1, "x", 0)
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") || strings.Contains(err.Error(), testToken) {
		t.Errorf("api error = %v", err)
	}

	// transport error: *url.Error prints the URL (token in the path) — must be stripped
	srv2, _ := fakeBotAPI(t, 200, `{}`)
	base := srv2.URL
	srv2.Close()
	err = NewClient(testToken, base, nil).SendMessage(context.Background(), 1, "x", 0)
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "/bot") {
		t.Errorf("transport error leaks the URL: %v", err)
	}

	// non-JSON answer
	srv3, _ := fakeBotAPI(t, 502, `<html>bad gateway</html>`)
	err = NewClient(testToken, srv3.URL, srv3.Client()).SendMessage(context.Background(), 1, "x", 0)
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("non-JSON error = %v", err)
	}

	// a cause that echoes the token is masked
	c := NewClient(testToken, "", nil)
	if e := c.redact("getMe", io.ErrUnexpectedEOF); strings.Contains(e.Error(), testToken) {
		t.Errorf("redact = %v", e)
	}
	if e := c.redact("getMe", &strErr{"dial " + testToken}); strings.Contains(e.Error(), testToken) ||
		!strings.Contains(e.Error(), "<token>") {
		t.Errorf("redact = %v", e)
	}
}

type strErr struct{ s string }

func (e *strErr) Error() string { return e.s }

func TestClientButtonsCallbackEditPhotoCheck(t *testing.T) {
	srv, calls := fakeBotAPI(t, 200, `{"ok":true,"result":{"total_count":2}}`)
	c := NewClient(testToken, srv.URL, srv.Client())
	ctx := context.Background()
	if err := c.SendButtons(ctx, 7, "ask", 3, []Button{{Text: "✓ ยืนยัน", Data: "li:x"}}); err != nil {
		t.Fatal(err)
	}
	b := (*calls)[0].body
	kb := b["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)[0].(map[string]any)
	if (*calls)[0].path != "/bot"+testToken+"/sendMessage" || kb["text"] != "✓ ยืนยัน" || kb["callback_data"] != "li:x" {
		t.Errorf("buttons = %v", b)
	}
	if err := c.AnswerCallback(ctx, "cb1", "หมดอายุ"); err != nil || (*calls)[1].body["callback_query_id"] != "cb1" ||
		(*calls)[1].body["text"] != "หมดอายุ" {
		t.Errorf("answer = %v %v", (*calls)[1].body, err)
	}
	if err := c.EditText(ctx, 7, 90, "done"); err != nil || (*calls)[2].path != "/bot"+testToken+"/editMessageText" ||
		(*calls)[2].body["message_id"] != float64(90) {
		t.Errorf("edit = %v %v", (*calls)[2], err)
	}
	if has, err := c.HasProfilePhoto(ctx, 7); err != nil || !has {
		t.Errorf("has photo = %v %v", has, err)
	}
}
