package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type sent struct {
	chatID  int64
	text    string
	replyTo int64
	buttons []Button
}

type fakeSender struct {
	mu       sync.Mutex
	sent     []sent
	answered []string // callback answers (their notice text)
	edits    []string
	err      error
}

func (f *fakeSender) SendMessage(_ context.Context, chatID int64, text string, replyTo int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sent{chatID, text, replyTo, nil})
	return f.err
}

func (f *fakeSender) SendButtons(_ context.Context, chatID int64, text string, replyTo int64, b []Button) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sent{chatID, text, replyTo, b})
	return f.err
}

func (f *fakeSender) AnswerCallback(_ context.Context, _ string, text string) error {
	f.answered = append(f.answered, text)
	return nil
}

func (f *fakeSender) EditText(_ context.Context, _, _ int64, text string) error {
	f.edits = append(f.edits, text)
	return nil
}

// fakeStore keeps by update_id like the table's unique key.
type fakeStore struct {
	rows map[int64]Received
	err  error
}

func (f *fakeStore) Save(_ context.Context, r Received) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if _, ok := f.rows[r.UpdateID]; ok {
		return false, nil
	}
	f.rows[r.UpdateID] = r
	return true, nil
}

type fakeLink struct {
	user   uuid.UUID
	chatOn bool
}

// fakeAccounts: linked chats.
type fakeAccounts struct {
	chats     map[int64]fakeLink
	lastLogin map[int64]bool // chats whose account would be locked out
	err       error
}

func (f *fakeAccounts) TelegramUser(_ context.Context, chatID int64) (uuid.UUID, bool, bool, error) {
	if f.err != nil {
		return uuid.Nil, false, false, f.err
	}
	l, ok := f.chats[chatID]
	return l.user, l.chatOn, ok, nil
}

func (f *fakeAccounts) TelegramUnlink(_ context.Context, chatID int64) (bool, error) {
	if f.lastLogin[chatID] {
		return false, shared.ErrLastLoginMethod
	}
	_, ok := f.chats[chatID]
	delete(f.chats, chatID)
	return ok, nil
}

var (
	nongID  = uuid.MustParse("0191c2a0-0000-7000-8000-0000000000a1")
	otherID = uuid.MustParse("0191c2a0-0000-7000-8000-0000000000a2")
)

const linkedChat, offChat, strangerChat = int64(1001), int64(1002), int64(2002)

func newAccounts() *fakeAccounts {
	return &fakeAccounts{
		chats:     map[int64]fakeLink{linkedChat: {nongID, true}, offChat: {otherID, false}},
		lastLogin: map[int64]bool{},
	}
}

func callback(updateID, chatID, fromID int64, data string) Update {
	return Update{UpdateID: updateID, CallbackQuery: &CallbackQuery{ID: "cb1", From: User{ID: fromID}, Data: data,
		Message: &Message{MessageID: 90, Chat: Chat{ID: chatID, Type: "private"}}}}
}

// A tap on an old login button (a message sent before 0.4.2-4) is answered and nothing else.
func TestOldButtonsAreJustAnswered(t *testing.T) {
	ctx := context.Background()
	h, s, _, _, _ := newTestHandler(t)
	if got := h.HandleUpdate(ctx, callback(1, linkedChat, linkedChat, "li:0191c2a0-0000-7000-8000-0000000000c1")); got != OutcomeIgnored {
		t.Errorf("old button = %s", got)
	}
	if len(s.answered) != 1 || s.answered[0] != "" || len(s.edits) != 0 || len(s.sent) != 0 {
		t.Errorf("answered %v, edits %v, sent %v", s.answered, s.edits, s.sent)
	}
	cb := callback(2, linkedChat, linkedChat, "li:x")
	cb.CallbackQuery.Message.Chat.Type = "group"
	if got := h.HandleUpdate(ctx, cb); got != OutcomeIgnored || len(s.answered) != 1 {
		t.Errorf("group callback = %s", got)
	}
}

func TestLastLoginMethodGuard(t *testing.T) {
	ctx := context.Background()
	h, s, _, acc, _ := newTestHandler(t)
	acc.lastLogin[linkedChat] = true
	if got := h.HandleUpdate(ctx, Update{UpdateID: 1, Message: msg(linkedChat, "private", "/unlink")}); got != OutcomeLastLogin ||
		s.sent[0].text != TextLastLogin {
		t.Errorf("/unlink of the only way in = %s %+v", got, s.sent)
	}
	if _, ok := acc.chats[linkedChat]; !ok {
		t.Errorf("unlinked anyway")
	}
}

func newTestHandler(t *testing.T) (*Handler, *fakeSender, *fakeStore, *fakeAccounts, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	s, st, acc := &fakeSender{}, &fakeStore{rows: map[int64]Received{}}, newAccounts()
	return NewHandler(s, st, acc, slog.New(slog.NewJSONHandler(&logs, nil))), s, st, acc, &logs
}

func msg(chatID int64, chatType, text string) *Message {
	return &Message{MessageID: 77, Chat: Chat{ID: chatID, Type: chatType}, Date: 1761631200,
		From: &User{ID: chatID, Username: "nong", FirstName: "Nong"}, Text: text}
}

func TestHandleUpdateTable(t *testing.T) {
	photo := msg(linkedChat, "private", "")
	photo.Photo = json.RawMessage(`[{"file_id":"x"}]`)

	cases := []struct {
		name  string
		u     Update
		out   Outcome
		reply string // "" = no reply
		kept  bool
	}{
		{"linked text", Update{UpdateID: 1, Message: msg(linkedChat, "private", "ข้าว 60")},
			OutcomeReceived, TextReceived, true},
		{"linked photo", Update{UpdateID: 2, Message: photo}, OutcomeNotText, TextNotText, false},
		{"unlinked text", Update{UpdateID: 3, Message: msg(strangerChat, "private", "hi")},
			OutcomeLinkFirst, TextLinkFirst, false},
		{"start linked", Update{UpdateID: 4, Message: msg(linkedChat, "private", "/start")},
			OutcomeStart, TextWelcome, false},
		{"allow messages granted", Update{UpdateID: 20, Message: &Message{MessageID: 9, Chat: Chat{ID: strangerChat, Type: "private"},
			WriteAccessAllowed: []byte(`{"from_request":false}`)}},
			OutcomeWriteOK, "", false},
		{"start unlinked", Update{UpdateID: 5, Message: msg(strangerChat, "private", "/start")},
			OutcomeLinkFirst, TextLinkFirst, false},
		{"start old code, unlinked", Update{UpdateID: 6, Message: msg(strangerChat, "private", "/start GoodCode_123")},
			OutcomeLinkFirst, TextLinkFirst, false},
		{"start@bot old code, linked", Update{UpdateID: 7, Message: msg(linkedChat, "private", "/start@chubiPocket_bot login_x")},
			OutcomeStart, TextWelcome, false},
		{"chat permission off", Update{UpdateID: 9, Message: msg(offChat, "private", "ข้าว 60")},
			OutcomeChatOff, TextChatOff, false},
		{"unlink linked", Update{UpdateID: 10, Message: msg(linkedChat, "private", "/unlink")},
			OutcomeUnlinked, TextUnlinked, false},
		{"unlink unlinked", Update{UpdateID: 11, Message: msg(strangerChat, "private", "/unlink")},
			OutcomeLinkFirst, TextLinkFirst, false},
		{"group", Update{UpdateID: 12, Message: msg(linkedChat, "group", "hi")}, OutcomeIgnored, "", false},
		{"channel", Update{UpdateID: 13, Message: msg(linkedChat, "channel", "hi")}, OutcomeIgnored, "", false},
		{"edited message", Update{UpdateID: 14, EditedMessage: msg(linkedChat, "private", "hi")},
			OutcomeIgnored, "", false},
		{"empty update", Update{UpdateID: 15}, OutcomeIgnored, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s, st, _, _ := newTestHandler(t)
			if got := h.HandleUpdate(context.Background(), tc.u); got != tc.out {
				t.Errorf("outcome = %s, want %s", got, tc.out)
			}
			switch {
			case tc.reply == "" && len(s.sent) != 0:
				t.Errorf("replied %v, want none", s.sent)
			case tc.reply != "":
				if len(s.sent) != 1 || s.sent[0].text != tc.reply || s.sent[0].replyTo != 77 ||
					s.sent[0].chatID != tc.u.Message.Chat.ID {
					t.Errorf("sent = %+v, want %q to the message", s.sent, tc.reply)
				}
			}
			r, kept := st.rows[tc.u.UpdateID]
			if kept != tc.kept {
				t.Errorf("kept = %v, want %v", kept, tc.kept)
			}
			if kept && (r.ChatID != linkedChat || r.Text != "ข้าว 60" || r.FromUsername != "nong" ||
				r.ReceivedAt.Unix() != 1761631200 || r.UserID == nil || *r.UserID != nongID) {
				t.Errorf("kept row = %+v", r)
			}
		})
	}
}

func TestUseThenUnlink(t *testing.T) {
	h, s, st, _, _ := newTestHandler(t)
	ctx := context.Background()
	if got := h.HandleUpdate(ctx, Update{UpdateID: 3, Message: msg(linkedChat, "private", "กาแฟ 45")}); got != OutcomeReceived {
		t.Errorf("linked = %s", got)
	}
	if r := st.rows[3]; r.UserID == nil || *r.UserID != nongID {
		t.Errorf("kept for %v", r.UserID)
	}
	h.HandleUpdate(ctx, Update{UpdateID: 4, Message: msg(linkedChat, "private", "/unlink")})
	if got := h.HandleUpdate(ctx, Update{UpdateID: 5, Message: msg(linkedChat, "private", "กาแฟ 45")}); got != OutcomeLinkFirst {
		t.Errorf("after unlink = %s", got)
	}
	if len(s.sent) != 3 {
		t.Errorf("replies = %d, want 3", len(s.sent))
	}
}

func TestHandleUpdateDedupe(t *testing.T) {
	h, s, _, acc, _ := newTestHandler(t)
	u := Update{UpdateID: 50, Message: msg(linkedChat, "private", "กาแฟ 45")}
	if got := h.HandleUpdate(context.Background(), u); got != OutcomeReceived {
		t.Fatalf("first = %s", got)
	}
	if got := h.HandleUpdate(context.Background(), u); got != OutcomeDuplicate {
		t.Errorf("second = %s, want duplicate", got)
	}
	if len(s.sent) != 1 {
		t.Errorf("replies = %d, want 1", len(s.sent))
	}

	// after a restart the memory is empty: the table's unique key still stops the second reply
	h2 := NewHandler(s, h.store, acc, h.log)
	if got := h2.HandleUpdate(context.Background(), u); got != OutcomeDuplicate {
		t.Errorf("after restart = %s, want duplicate", got)
	}
	if len(s.sent) != 1 {
		t.Errorf("replies after restart = %d, want 1", len(s.sent))
	}
}

func TestHandleUpdateFailures(t *testing.T) {
	h, s, st, acc, logs := newTestHandler(t)
	st.err = errors.New("db down")
	if got := h.HandleUpdate(context.Background(), Update{UpdateID: 60, Message: msg(linkedChat, "private", "x")}); got != OutcomeReceived {
		t.Errorf("outcome = %s", got)
	}
	if len(s.sent) != 1 {
		t.Errorf("no reply when the store fails")
	}
	s.err = errors.New("telegram down")
	h.HandleUpdate(context.Background(), Update{UpdateID: 61, Message: msg(strangerChat, "private", "x")})
	if !strings.Contains(logs.String(), "reply failed") {
		t.Errorf("send failure not logged")
	}
	// a lookup failure: no reply, logged
	acc.err = errors.New("db down")
	n := len(s.sent)
	if got := h.HandleUpdate(context.Background(), Update{UpdateID: 62, Message: msg(linkedChat, "private", "x")}); got != OutcomeError {
		t.Errorf("lookup failure = %s", got)
	}
	if len(s.sent) != n || !strings.Contains(logs.String(), "lookup failed") {
		t.Errorf("lookup failure replied or wasn't logged")
	}
}

func TestLogLinesNeverHoldTheText(t *testing.T) {
	h, _, _, _, logs := newTestHandler(t)
	secretText := "ค่าเช่าห้อง 4500 รหัส 9988"
	h.HandleUpdate(context.Background(), Update{UpdateID: 70, Message: msg(linkedChat, "private", secretText)})
	h.HandleUpdate(context.Background(), Update{UpdateID: 71, Message: msg(strangerChat, "private", secretText)})
	h.HandleUpdate(context.Background(), Update{UpdateID: 70, Message: msg(linkedChat, "private", secretText)})
	h.HandleUpdate(context.Background(), Update{UpdateID: 72, Message: msg(linkedChat, "private", "/start GoodCode_123")})
	out := logs.String()
	if strings.Contains(out, "9988") || strings.Contains(out, "ค่าเช่า") || strings.Contains(out, "GoodCode") {
		t.Errorf("log holds the text or a code: %s", out)
	}
	for _, want := range []string{`"update_id":70`, `"chat_id":1001`, `"linked":true`, `"kind":"text"`,
		`"text_len":26`, `"outcome":"received"`, `"outcome":"link_first"`, `"outcome":"duplicate"`, `"outcome":"start"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log misses %s: %s", want, out)
		}
	}
}

func TestCommand(t *testing.T) {
	cases := []struct {
		text, name, arg string
		ok              bool
	}{
		{"/start", "/start", "", true},
		{"/start abc", "/start", "abc", true},
		{"/start@chubiPocket_bot", "/start", "", true},
		{"/start@chubiPocket_bot  abc ", "/start", "abc", true},
		{"/started", "/start", "", false},
		{"start", "/start", "", false},
		{"/unlink", "/unlink", "", true},
	}
	for _, c := range cases {
		arg, ok := command(c.text, c.name)
		if arg != c.arg || ok != c.ok {
			t.Errorf("command(%q) = %q, %v; want %q, %v", c.text, arg, ok, c.arg, c.ok)
		}
	}
}

func TestSeenSetForgetsTheOldest(t *testing.T) {
	s := newSeenSet(2)
	if !s.add(1) || !s.add(2) || s.add(1) {
		t.Fatal("basic add")
	}
	s.add(3) // 1 drops out
	if !s.add(1) {
		t.Errorf("1 should be forgotten")
	}
	if s.add(3) {
		t.Errorf("3 should be remembered")
	}
}

func TestPickPhotoSize(t *testing.T) {
	sizes := []PhotoSize{{"a", 160, 160}, {"b", 320, 320}, {"c", 640, 640}}
	if got := pickPhotoSize(sizes); got.FileID != "a" {
		t.Errorf("pick = %s, want the 160 one", got.FileID)
	}
	if got := pickPhotoSize([]PhotoSize{{"x", 640, 640}, {"y", 200, 200}}); got.FileID != "y" {
		t.Errorf("pick = %s, want the smallest ≥ 160", got.FileID)
	}
	if got := pickPhotoSize([]PhotoSize{{"s", 80, 80}, {"m", 120, 120}}); got.FileID != "m" {
		t.Errorf("pick = %s, want the widest when none is ≥ 160", got.FileID)
	}
}
