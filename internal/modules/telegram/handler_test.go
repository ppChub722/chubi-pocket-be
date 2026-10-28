package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

type sent struct {
	chatID  int64
	text    string
	replyTo int64
}

type fakeSender struct {
	mu   sync.Mutex
	sent []sent
	err  error
}

func (f *fakeSender) SendMessage(_ context.Context, chatID int64, text string, replyTo int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sent{chatID, text, replyTo})
	return f.err
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

const allowedChat, otherChat = int64(1001), int64(2002)

func newTestHandler(t *testing.T) (*Handler, *fakeSender, *fakeStore, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	s, st := &fakeSender{}, &fakeStore{rows: map[int64]Received{}}
	return NewHandler(s, st, []int64{allowedChat}, slog.New(slog.NewJSONHandler(&logs, nil))), s, st, &logs
}

func msg(chatID int64, chatType, text string) *Message {
	return &Message{MessageID: 77, Chat: Chat{ID: chatID, Type: chatType}, Date: 1761631200,
		From: &User{ID: 5, Username: "nong"}, Text: text}
}

func TestHandleUpdateTable(t *testing.T) {
	photo := msg(allowedChat, "private", "")
	photo.Photo = json.RawMessage(`[{"file_id":"x"}]`)

	cases := []struct {
		name  string
		u     Update
		out   Outcome
		reply string // "" = no reply
		kept  bool
	}{
		{"allowed text", Update{UpdateID: 1, Message: msg(allowedChat, "private", "ข้าว 60")},
			OutcomeReceived, TextReceived, true},
		{"allowed photo", Update{UpdateID: 2, Message: photo}, OutcomeNotText, TextNotText, false},
		{"not allowed", Update{UpdateID: 3, Message: msg(otherChat, "private", "hi")},
			OutcomeNotAllowed, fmt.Sprintf(TextNotAllowed, otherChat), false},
		{"start allowed", Update{UpdateID: 4, Message: msg(allowedChat, "private", "/start")},
			OutcomeStart, TextHello, false},
		{"start with payload", Update{UpdateID: 5, Message: msg(allowedChat, "private", "/start abc")},
			OutcomeStart, TextHello, false},
		{"start not allowed", Update{UpdateID: 6, Message: msg(otherChat, "private", "/start")},
			OutcomeNotAllowed, fmt.Sprintf(TextNotAllowed, otherChat), false},
		{"group", Update{UpdateID: 7, Message: msg(allowedChat, "group", "hi")}, OutcomeIgnored, "", false},
		{"channel", Update{UpdateID: 8, Message: msg(allowedChat, "channel", "hi")}, OutcomeIgnored, "", false},
		{"edited message", Update{UpdateID: 9, EditedMessage: msg(allowedChat, "private", "hi")},
			OutcomeIgnored, "", false},
		{"empty update", Update{UpdateID: 10}, OutcomeIgnored, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s, st, _ := newTestHandler(t)
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
			if kept && (r.ChatID != allowedChat || r.Text != "ข้าว 60" || r.FromUsername != "nong" ||
				r.ReceivedAt.Unix() != 1761631200) {
				t.Errorf("kept row = %+v", r)
			}
		})
	}
}

func TestHandleUpdateDedupe(t *testing.T) {
	h, s, _, _ := newTestHandler(t)
	u := Update{UpdateID: 50, Message: msg(allowedChat, "private", "กาแฟ 45")}
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
	h2 := NewHandler(s, h.store, []int64{allowedChat}, h.log)
	if got := h2.HandleUpdate(context.Background(), u); got != OutcomeDuplicate {
		t.Errorf("after restart = %s, want duplicate", got)
	}
	if len(s.sent) != 1 {
		t.Errorf("replies after restart = %d, want 1", len(s.sent))
	}
}

func TestHandleUpdateStoreOrSendFailureStillAnswers(t *testing.T) {
	h, s, st, logs := newTestHandler(t)
	st.err = errors.New("db down")
	if got := h.HandleUpdate(context.Background(), Update{UpdateID: 60, Message: msg(allowedChat, "private", "x")}); got != OutcomeReceived {
		t.Errorf("outcome = %s", got)
	}
	if len(s.sent) != 1 {
		t.Errorf("no reply when the store fails")
	}
	s.err = errors.New("telegram down")
	h.HandleUpdate(context.Background(), Update{UpdateID: 61, Message: msg(otherChat, "private", "x")})
	if !strings.Contains(logs.String(), "reply failed") {
		t.Errorf("send failure not logged")
	}
}

func TestLogLinesNeverHoldTheText(t *testing.T) {
	h, _, _, logs := newTestHandler(t)
	secretText := "ค่าเช่าห้อง 4500 รหัส 9988"
	h.HandleUpdate(context.Background(), Update{UpdateID: 70, Message: msg(allowedChat, "private", secretText)})
	h.HandleUpdate(context.Background(), Update{UpdateID: 71, Message: msg(otherChat, "private", secretText)})
	h.HandleUpdate(context.Background(), Update{UpdateID: 70, Message: msg(allowedChat, "private", secretText)})
	out := logs.String()
	if strings.Contains(out, "9988") || strings.Contains(out, "ค่าเช่า") {
		t.Errorf("log holds the text: %s", out)
	}
	for _, want := range []string{`"update_id":70`, `"chat_id":1001`, `"allowed":true`, `"kind":"text"`,
		`"text_len":26`, `"outcome":"received"`, `"outcome":"not_allowed"`, `"outcome":"duplicate"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log misses %s: %s", want, out)
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

func TestParseChatIDs(t *testing.T) {
	var logs bytes.Buffer
	got := ParseChatIDs(" 123, -456,,abc ,789", slog.New(slog.NewJSONHandler(&logs, nil)))
	if fmt.Sprint(got) != "[123 -456 789]" {
		t.Errorf("ids = %v", got)
	}
	if !strings.Contains(logs.String(), "abc") {
		t.Errorf("bad entry not logged")
	}
	if ParseChatIDs("", slog.Default()) != nil {
		t.Errorf("empty = nobody")
	}
}
