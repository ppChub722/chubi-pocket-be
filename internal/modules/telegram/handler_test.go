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

// fakeAccounts: linked chats + one-time codes (a code binds once).
type fakeAccounts struct {
	chats      map[int64]fakeLink
	codes      map[string]uuid.UUID
	names      map[uuid.UUID]string
	accounts   map[int64]string // the account label each bind got
	loginCodes map[string]uuid.UUID
	loginChat  map[uuid.UUID]int64 // which chat opened a login code
	confirmed  map[uuid.UUID]bool
	lastLogin  map[int64]bool // chats whose account would be locked out
	err        error
}

func (f *fakeAccounts) TelegramLoginOpen(_ context.Context, code string, chatID int64, _, _ string) (uuid.UUID, string, bool, error) {
	id, ok := f.loginCodes[code]
	if !ok {
		return uuid.Nil, "", false, nil
	}
	f.loginChat[id] = chatID
	name := ""
	if l, linked := f.chats[chatID]; linked {
		name = f.names[l.user]
	}
	return id, name, true, nil
}

func (f *fakeAccounts) TelegramLoginConfirm(_ context.Context, id uuid.UUID, chatID int64) (bool, error) {
	if f.loginChat[id] != chatID {
		return false, nil
	}
	f.confirmed[id] = true
	return true, nil
}

func (f *fakeAccounts) TelegramUser(_ context.Context, chatID int64) (uuid.UUID, bool, bool, error) {
	if f.err != nil {
		return uuid.Nil, false, false, f.err
	}
	l, ok := f.chats[chatID]
	return l.user, l.chatOn, ok, nil
}

func (f *fakeAccounts) TelegramBind(_ context.Context, code string, chatID int64, account string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	user, ok := f.codes[code]
	if !ok {
		return "", false, nil
	}
	if f.lastLogin[chatID] {
		return "", false, shared.ErrLastLoginMethod
	}
	delete(f.codes, code)
	f.chats[chatID] = fakeLink{user: user, chatOn: true}
	f.accounts[chatID] = account
	return f.names[user], true, nil
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
		chats:      map[int64]fakeLink{linkedChat: {nongID, true}, offChat: {otherID, false}},
		codes:      map[string]uuid.UUID{"GoodCode_123": nongID},
		names:      map[uuid.UUID]string{nongID: "น้อง", otherID: "Beam"},
		accounts:   map[int64]string{},
		loginCodes: map[string]uuid.UUID{"LoginCode_1": loginCodeID},
		loginChat:  map[uuid.UUID]int64{},
		confirmed:  map[uuid.UUID]bool{},
		lastLogin:  map[int64]bool{},
	}
}

var loginCodeID = uuid.MustParse("0191c2a0-0000-7000-8000-0000000000c1")

func callback(updateID, chatID, fromID int64, data string) Update {
	return Update{UpdateID: updateID, CallbackQuery: &CallbackQuery{ID: "cb1", From: User{ID: fromID}, Data: data,
		Message: &Message{MessageID: 90, Chat: Chat{ID: chatID, Type: "private"}}}}
}

func TestLoginWithTelegram(t *testing.T) {
	ctx := context.Background()

	// a linked chat: asked to confirm as its account, then taps ✓ ยืนยัน
	h, s, _, acc, logs := newTestHandler(t)
	if got := h.HandleUpdate(ctx, Update{UpdateID: 1, Message: msg(linkedChat, "private", "/start login_LoginCode_1")}); got != OutcomeLoginAsk {
		t.Fatalf("open = %s", got)
	}
	if len(s.sent) != 1 || s.sent[0].text != "เข้าสู่ระบบ chubi-pocket เป็น น้อง ไหมครับ?" ||
		len(s.sent[0].buttons) != 1 || s.sent[0].buttons[0].Text != ButtonConfirm ||
		s.sent[0].buttons[0].Data != "li:"+loginCodeID.String() {
		t.Fatalf("ask = %+v", s.sent)
	}
	if got := h.HandleUpdate(ctx, callback(2, linkedChat, linkedChat, s.sent[0].buttons[0].Data)); got != OutcomeLoginOK {
		t.Errorf("confirm = %s", got)
	}
	if !acc.confirmed[loginCodeID] || len(s.edits) != 1 || s.edits[0] != TextLoginDone || s.answered[0] != "" {
		t.Errorf("after confirm: confirmed %v edits %v answers %v", acc.confirmed, s.edits, s.answered)
	}
	if strings.Contains(logs.String(), "LoginCode_1") {
		t.Errorf("the code is in the log")
	}

	// an unlinked chat: offered sign-up
	h, s, _, acc, _ = newTestHandler(t)
	h.HandleUpdate(ctx, Update{UpdateID: 3, Message: msg(strangerChat, "private", "/start login_LoginCode_1")})
	if len(s.sent) != 1 || s.sent[0].text != TextLoginNoAccount || s.sent[0].buttons[0].Text != ButtonSignup ||
		s.sent[0].buttons[0].Data != "su:"+loginCodeID.String() {
		t.Fatalf("signup offer = %+v", s.sent)
	}
	h.HandleUpdate(ctx, callback(4, strangerChat, strangerChat, s.sent[0].buttons[0].Data))
	if !acc.confirmed[loginCodeID] || s.edits[0] != TextSignupDone {
		t.Errorf("signup tap: %v %v", acc.confirmed, s.edits)
	}

	// a bad code; a tap from another chat / with junk data / twice
	h, s, _, acc, _ = newTestHandler(t)
	if got := h.HandleUpdate(ctx, Update{UpdateID: 5, Message: msg(linkedChat, "private", "/start login_nope")}); got != OutcomeLoginBad ||
		s.sent[0].text != TextLoginExpired {
		t.Errorf("bad login code = %s %+v", got, s.sent)
	}
	h.HandleUpdate(ctx, Update{UpdateID: 6, Message: msg(linkedChat, "private", "/start login_LoginCode_1")})
	if got := h.HandleUpdate(ctx, callback(7, strangerChat, strangerChat, "li:"+loginCodeID.String())); got != OutcomeLoginBad {
		t.Errorf("another chat's tap = %s", got)
	}
	if got := h.HandleUpdate(ctx, callback(8, linkedChat, linkedChat, "li:not-a-uuid")); got != OutcomeLoginBad {
		t.Errorf("junk data = %s", got)
	}
	if got := h.HandleUpdate(ctx, callback(8, linkedChat, linkedChat, "li:"+loginCodeID.String())); got != OutcomeDuplicate {
		t.Errorf("a resent tap = %s", got)
	}
	if acc.confirmed[loginCodeID] {
		t.Errorf("confirmed by a bad tap")
	}
	if s.answered[0] != TextLoginExpired || s.edits[0] != TextLoginExpired {
		t.Errorf("bad tap notice = %v / %v", s.answered, s.edits)
	}
	// a callback in a group is ignored
	cb := callback(9, linkedChat, linkedChat, "li:"+loginCodeID.String())
	cb.CallbackQuery.Message.Chat.Type = "group"
	if got := h.HandleUpdate(ctx, cb); got != OutcomeIgnored {
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
	if got := h.HandleUpdate(ctx, Update{UpdateID: 2, Message: msg(linkedChat, "private", "/start GoodCode_123")}); got != OutcomeLastLogin {
		t.Errorf("moving the only way in = %s", got)
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
			OutcomeStart, TextHello, false},
		{"start unlinked", Update{UpdateID: 5, Message: msg(strangerChat, "private", "/start")},
			OutcomeLinkFirst, TextLinkFirst, false},
		{"start good code", Update{UpdateID: 6, Message: msg(strangerChat, "private", "/start GoodCode_123")},
			OutcomeLinked, "เชื่อมกับบัญชี น้อง แล้ว ✓", false},
		{"start@bot good code", Update{UpdateID: 7, Message: msg(strangerChat, "private", "/start@chubiPocket_bot GoodCode_123")},
			OutcomeLinked, "เชื่อมกับบัญชี น้อง แล้ว ✓", false},
		{"start bad code", Update{UpdateID: 8, Message: msg(strangerChat, "private", "/start nope")},
			OutcomeBadCode, TextBadCode, false},
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

func TestLinkThenUseThenUnlink(t *testing.T) {
	h, s, st, acc, _ := newTestHandler(t)
	ctx := context.Background()
	h.HandleUpdate(ctx, Update{UpdateID: 1, Message: msg(strangerChat, "private", "/start GoodCode_123")})
	if acc.accounts[strangerChat] != "@nong" {
		t.Errorf("account label = %q, want @nong", acc.accounts[strangerChat])
	}
	// the code is single use
	if got := h.HandleUpdate(ctx, Update{UpdateID: 2, Message: msg(3003, "private", "/start GoodCode_123")}); got != OutcomeBadCode {
		t.Errorf("reused code = %s", got)
	}
	if got := h.HandleUpdate(ctx, Update{UpdateID: 3, Message: msg(strangerChat, "private", "กาแฟ 45")}); got != OutcomeReceived {
		t.Errorf("after linking = %s", got)
	}
	if r := st.rows[3]; r.UserID == nil || *r.UserID != nongID {
		t.Errorf("kept for %v", r.UserID)
	}
	h.HandleUpdate(ctx, Update{UpdateID: 4, Message: msg(strangerChat, "private", "/unlink")})
	if got := h.HandleUpdate(ctx, Update{UpdateID: 5, Message: msg(strangerChat, "private", "กาแฟ 45")}); got != OutcomeLinkFirst {
		t.Errorf("after unlink = %s", got)
	}
	if len(s.sent) != 5 {
		t.Errorf("replies = %d, want 5", len(s.sent))
	}

	// no username → the first name
	acc.codes["Second_Code"] = otherID
	m := msg(4004, "private", "/start Second_Code")
	m.From = &User{ID: 4004, FirstName: "Beam"}
	h.HandleUpdate(ctx, Update{UpdateID: 6, Message: m})
	if acc.accounts[4004] != "Beam" {
		t.Errorf("account label = %q, want Beam", acc.accounts[4004])
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
	h.HandleUpdate(context.Background(), Update{UpdateID: 72, Message: msg(strangerChat, "private", "/start GoodCode_123")})
	out := logs.String()
	if strings.Contains(out, "9988") || strings.Contains(out, "ค่าเช่า") || strings.Contains(out, "GoodCode") {
		t.Errorf("log holds the text or a code: %s", out)
	}
	for _, want := range []string{`"update_id":70`, `"chat_id":1001`, `"linked":true`, `"kind":"text"`,
		`"text_len":26`, `"outcome":"received"`, `"outcome":"link_first"`, `"outcome":"duplicate"`, `"outcome":"linked"`} {
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
