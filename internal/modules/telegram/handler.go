package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Reply texts (0.4.2 plan: 1a table + "1b BUILD PLAN" B5).
const (
	TextReceived  = "ได้รับข้อความแล้ว ✓"
	TextNotText   = "ตอนนี้รับแค่ข้อความตัวอักษรนะครับ"
	TextHello     = "สวัสดีครับ ส่งข้อความมาได้เลย"
	TextLinkFirst = "เชื่อมบัญชีก่อนนะครับ: แอป → โปรไฟล์ → Telegram"
	TextChatOff   = "ปิดการจดรายการไว้ เปิดได้ในแอป"
	TextUnlinked  = "ยกเลิกการเชื่อมแล้ว"
	TextLastLogin = "ตั้งรหัสผ่านในแอปก่อน ไม่งั้นจะเข้าบัญชีไม่ได้"

	// TextWelcome — sent once a Telegram is linked (0.4.2-4 OIDC login / sign-up / link) and as the
	// answer to a plain /start from a linked chat.
	TextWelcome = "สวัสดีครับ เชื่อม Telegram กับ chubiPocket แล้ว ✓\n\n" +
		"พิมพ์รายการมาได้เลย เช่น\n" +
		"ข้าวมันไก่ 60\n" +
		"กาแฟ 55 บัตรเครดิต\n" +
		"เงินเดือน 30000\n\n" +
		"เปิด / ปิดการจดผ่านแชทได้ในแอป: โปรไฟล์ → Telegram"

)

// Outcome is what HandleUpdate did with an update (the log line's "outcome").
type Outcome string

const (
	OutcomeIgnored   Outcome = "ignored"    // not a private-chat message: no reply
	OutcomeDuplicate Outcome = "duplicate"  // update_id seen before: no reply
	OutcomeReceived  Outcome = "received"   // linked, chat on, text: kept + "ได้รับข้อความแล้ว ✓"
	OutcomeNotText   Outcome = "not_text"   // linked, photo / voice / sticker …
	OutcomeLinkFirst Outcome = "link_first" // the chat isn't linked to a user
	OutcomeChatOff   Outcome = "chat_off"   // linked, the chat permission is off
	OutcomeStart     Outcome = "start"      // /start from a linked chat
	OutcomeUnlinked  Outcome = "unlinked"   // /unlink from a linked chat
	OutcomeLastLogin Outcome = "last_login" // /unlink refused: the account's only way in
	OutcomeWriteOK   Outcome = "write_ok"   // "Allow Messages" granted at a Telegram login: no reply
	OutcomeError     Outcome = "error"      // a lookup failed: no reply
)

type sender interface {
	SendMessage(ctx context.Context, chatID int64, text string, replyTo int64) error
	SendButtons(ctx context.Context, chatID int64, text string, replyTo int64, buttons []Button) error
	AnswerCallback(ctx context.Context, callbackID, text string) error
	EditText(ctx context.Context, chatID, messageID int64, text string) error
}

type saver interface {
	Save(ctx context.Context, r Received) (bool, error)
}

// Accounts is the linking the bot reads and writes (connections.Service).
type Accounts interface {
	// TelegramUser — who a chat is linked to; linked false = nobody.
	TelegramUser(ctx context.Context, chatID int64) (userID uuid.UUID, chatOn, linked bool, err error)
	// TelegramUnlink — the chat unlinks itself; false = it wasn't linked.
	TelegramUnlink(ctx context.Context, chatID int64) (bool, error)
}

// Chatbot is the chatbot (0.4.2-4): texts of linked chats with the permission on, and taps on its
// own buttons (Tap false = not its button).
type Chatbot interface {
	Text(ctx context.Context, userID uuid.UUID, m *Message)
	Tap(ctx context.Context, cq *CallbackQuery) bool
}

// chatTimeout bounds one chatbot turn; it runs after Telegram has its 200 (a model call may take
// longer than the webhook should wait).
const chatTimeout = 60 * time.Second

// OutcomeChat — the text went to the chatbot.
const OutcomeChat Outcome = "chat"

// Handler holds the bot's logic; the webhook only decodes and calls HandleUpdate.
type Handler struct {
	send     sender
	store    saver // nil = texts are not kept
	accounts Accounts
	chat     Chatbot    // nil = texts get "ได้รับข้อความแล้ว ✓"
	lang     LangSource // nil = Telegram's language, else English
	seen     *seenSet
	log      *slog.Logger
}

func NewHandler(send sender, store saver, accounts Accounts, log *slog.Logger) *Handler {
	return &Handler{send: send, store: store, accounts: accounts, seen: newSeenSet(1000), log: log}
}

// HandleUpdate answers one update. Only private chats get a reply; linked = allowed (0.4.2 1b).
// An update_id is handled once (in memory, plus the table's unique key for kept texts). The log
// line never holds the text.
func (h *Handler) HandleUpdate(ctx context.Context, u Update) Outcome {
	if cq := u.CallbackQuery; cq != nil && cq.Message != nil && cq.Message.Chat.Type == "private" {
		if !h.seen.add(u.UpdateID) {
			h.logLine(u, cq.Message, false, OutcomeDuplicate)
			return OutcomeDuplicate
		}
		if h.chat != nil && h.chat.Tap(ctx, cq) {
			h.log.Info("telegram update", "update_id", u.UpdateID, "outcome", string(OutcomeChat),
				"chat_id", cq.Message.Chat.ID, "kind", "callback")
			return OutcomeChat
		}
		return h.callback(ctx, u, cq)
	}
	m := u.Message
	if m == nil || m.Chat.Type != "private" {
		h.logLine(u, m, false, OutcomeIgnored)
		return OutcomeIgnored
	}
	if !h.seen.add(u.UpdateID) {
		h.logLine(u, m, false, OutcomeDuplicate)
		return OutcomeDuplicate
	}
	// it usually comes before the app has sent us the login, so the chat is not linked yet
	if len(m.WriteAccessAllowed) > 0 {
		h.logLine(u, m, false, OutcomeWriteOK)
		return OutcomeWriteOK
	}

	userID, chatOn, linked, err := h.accounts.TelegramUser(ctx, m.Chat.ID)
	if err != nil {
		return h.failed(u, m, "lookup", err)
	}
	if _, ok := command(m.Text, "/unlink"); ok && linked {
		_, err := h.accounts.TelegramUnlink(ctx, m.Chat.ID)
		if errors.Is(err, shared.ErrLastLoginMethod) {
			return h.reply(ctx, u, m, true, h.texts(ctx, m).LastLogin, OutcomeLastLogin)
		}
		if err != nil {
			return h.failed(u, m, "unlink", err)
		}
		return h.reply(ctx, u, m, false, h.texts(ctx, m).Unlinked, OutcomeUnlinked)
	}
	switch {
	case !linked:
		return h.reply(ctx, u, m, false, h.texts(ctx, m).LinkFirst, OutcomeLinkFirst)
	case isCommand(m.Text, "/start"):
		return h.reply(ctx, u, m, true, h.texts(ctx, m).Welcome, OutcomeStart)
	case !chatOn:
		return h.reply(ctx, u, m, true, h.texts(ctx, m).ChatOff, OutcomeChatOff)
	case m.Text == "":
		return h.reply(ctx, u, m, true, h.texts(ctx, m).NotText, OutcomeNotText)
	}
	if h.store != nil {
		r := received(u.UpdateID, m)
		r.UserID = &userID
		kept, err := h.store.Save(ctx, r)
		if err != nil {
			// still answer: the user sees it arrived; the eval set just misses one line
			h.log.Error("telegram: keeping a text failed", "update_id", u.UpdateID, "error", err)
		} else if !kept {
			h.logLine(u, m, true, OutcomeDuplicate)
			return OutcomeDuplicate
		}
	}
	if h.chat != nil {
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), chatTimeout)
			defer cancel()
			h.chat.Text(cctx, userID, m)
		}()
		h.logLine(u, m, true, OutcomeChat)
		return OutcomeChat
	}
	return h.reply(ctx, u, m, true, h.texts(ctx, m).Received, OutcomeReceived)
}

// callback — a tap on a button that is not the chatbot's (old login buttons): just answered.
func (h *Handler) callback(ctx context.Context, u Update, cq *CallbackQuery) Outcome {
	if err := h.send.AnswerCallback(ctx, cq.ID, ""); err != nil {
		h.log.Error("telegram: answer callback failed", "update_id", u.UpdateID, "error", err)
	}
	h.log.Info("telegram update", "update_id", u.UpdateID, "outcome", string(OutcomeIgnored),
		"chat_id", cq.Message.Chat.ID, "kind", "callback")
	return OutcomeIgnored
}

// texts: the reply language of this chat (app setting → Telegram's → English).
func (h *Handler) texts(ctx context.Context, m *Message) Texts {
	app, tg := "", ""
	if h.lang != nil {
		app = h.lang.ChatLang(ctx, m.Chat.ID)
	}
	if m.From != nil {
		tg = m.From.LanguageCode
	}
	return TextsFor(Lang(app, tg))
}

func (h *Handler) reply(ctx context.Context, u Update, m *Message, linked bool, text string, out Outcome) Outcome {
	if err := h.send.SendMessage(ctx, m.Chat.ID, text, m.MessageID); err != nil {
		h.log.Error("telegram: reply failed", "update_id", u.UpdateID, "chat_id", m.Chat.ID, "error", err)
	}
	h.logLine(u, m, linked, out)
	return out
}

func (h *Handler) failed(u Update, m *Message, step string, err error) Outcome {
	h.log.Error("telegram: "+step+" failed", "update_id", u.UpdateID, "chat_id", m.Chat.ID, "error", err)
	h.logLine(u, m, false, OutcomeError)
	return OutcomeError
}

func (h *Handler) logLine(u Update, m *Message, linked bool, out Outcome) {
	attrs := []any{"update_id", u.UpdateID, "outcome", string(out)}
	if m != nil {
		attrs = append(attrs, "chat_id", m.Chat.ID, "chat_type", m.Chat.Type, "linked", linked,
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

// command — text is name ("/start", "/start@bot", "/start <arg>"); arg = what follows, trimmed.
func command(text, name string) (arg string, ok bool) {
	if !isCommand(text, name) {
		return "", false
	}
	rest := strings.TrimPrefix(text, name)
	if strings.HasPrefix(rest, "@") {
		if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
			rest = rest[i:]
		} else {
			rest = ""
		}
	}
	return strings.TrimSpace(rest), true
}

func isCommand(text, name string) bool {
	return text == name || strings.HasPrefix(text, name+" ") || strings.HasPrefix(text, name+"@")
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
