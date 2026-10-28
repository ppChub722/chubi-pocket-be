package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Reply texts (0.4.2 plan, "1a — Telegram hello").
const (
	TextReceived   = "ได้รับข้อความแล้ว ✓"
	TextNotText    = "ตอนนี้รับแค่ข้อความตัวอักษรนะครับ"
	TextNotAllowed = "ยังไม่เปิดให้ใช้ (chat id: %d)"
	TextHello      = "สวัสดีครับ ส่งข้อความมาได้เลย"
)

// Outcome is what HandleUpdate did with an update (the log line's "outcome").
type Outcome string

const (
	OutcomeIgnored    Outcome = "ignored"     // not a private-chat message: no reply
	OutcomeDuplicate  Outcome = "duplicate"   // update_id seen before: no reply
	OutcomeReceived   Outcome = "received"    // allowed text: kept + "ได้รับข้อความแล้ว ✓"
	OutcomeNotText    Outcome = "not_text"    // allowed, photo / voice / sticker …
	OutcomeNotAllowed Outcome = "not_allowed" // chat not in TELEGRAM_ALLOWED_CHAT_IDS
	OutcomeStart      Outcome = "start"       // /start from an allowed chat
)

type sender interface {
	SendMessage(ctx context.Context, chatID int64, text string, replyTo int64) error
}

type saver interface {
	Save(ctx context.Context, r Received) (bool, error)
}

// Handler holds the bot's logic; the webhook only decodes and calls HandleUpdate.
type Handler struct {
	send    sender
	store   saver // nil = texts are not kept
	allowed map[int64]bool
	seen    *seenSet
	log     *slog.Logger
}

func NewHandler(send sender, store saver, allowedChatIDs []int64, log *slog.Logger) *Handler {
	allowed := make(map[int64]bool, len(allowedChatIDs))
	for _, id := range allowedChatIDs {
		allowed[id] = true
	}
	return &Handler{send: send, store: store, allowed: allowed, seen: newSeenSet(1000), log: log}
}

// HandleUpdate answers one update. Only private chats get a reply; an update_id is handled once
// (in memory, plus the table's unique key for kept texts). The log line never holds the text.
func (h *Handler) HandleUpdate(ctx context.Context, u Update) Outcome {
	m := u.Message
	if m == nil || m.Chat.Type != "private" {
		h.logLine(u, m, false, OutcomeIgnored)
		return OutcomeIgnored
	}
	allowed := h.allowed[m.Chat.ID]
	if !h.seen.add(u.UpdateID) {
		h.logLine(u, m, allowed, OutcomeDuplicate)
		return OutcomeDuplicate
	}

	var reply string
	var out Outcome
	switch {
	case !allowed:
		reply, out = fmt.Sprintf(TextNotAllowed, m.Chat.ID), OutcomeNotAllowed
	case isStart(m.Text):
		reply, out = TextHello, OutcomeStart
	case m.Text == "":
		reply, out = TextNotText, OutcomeNotText
	default:
		reply, out = TextReceived, OutcomeReceived
		if h.store != nil {
			kept, err := h.store.Save(ctx, received(u.UpdateID, m))
			if err != nil {
				// still answer: the user sees it arrived; the eval set just misses one line
				h.log.Error("telegram: keeping a text failed", "update_id", u.UpdateID, "error", err)
			} else if !kept {
				h.logLine(u, m, allowed, OutcomeDuplicate)
				return OutcomeDuplicate
			}
		}
	}

	if err := h.send.SendMessage(ctx, m.Chat.ID, reply, m.MessageID); err != nil {
		h.log.Error("telegram: reply failed", "update_id", u.UpdateID, "chat_id", m.Chat.ID, "error", err)
	}
	h.logLine(u, m, allowed, out)
	return out
}

func (h *Handler) logLine(u Update, m *Message, allowed bool, out Outcome) {
	attrs := []any{"update_id", u.UpdateID, "outcome", string(out)}
	if m != nil {
		attrs = append(attrs, "chat_id", m.Chat.ID, "chat_type", m.Chat.Type, "allowed", allowed,
			"kind", m.Kind(), "text_len", utf8.RuneCountInString(m.Text))
	}
	h.log.Info("telegram update", attrs...)
}

func received(updateID int64, m *Message) Received {
	r := Received{UpdateID: updateID, ChatID: m.Chat.ID, Text: m.Text, ReceivedAt: time.Unix(m.Date, 0).UTC()}
	if m.Date == 0 {
		r.ReceivedAt = time.Now().UTC()
	}
	if m.From != nil {
		r.FromUsername = m.From.Username
	}
	return r
}

// isStart: "/start", "/start <payload>" or "/start@<bot>".
func isStart(text string) bool {
	return text == "/start" || strings.HasPrefix(text, "/start ") || strings.HasPrefix(text, "/start@")
}

// seenSet remembers the last max update ids.
type seenSet struct {
	mu    sync.Mutex
	ids   map[int64]struct{}
	order []int64
	max   int
}

func newSeenSet(max int) *seenSet {
	return &seenSet{ids: make(map[int64]struct{}, max), max: max}
}

// add reports false when id was seen before.
func (s *seenSet) add(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; ok {
		return false
	}
	if len(s.order) >= s.max {
		delete(s.ids, s.order[0])
		s.order = s.order[1:]
	}
	s.ids[id] = struct{}{}
	s.order = append(s.order, id)
	return true
}
