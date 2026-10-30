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
	TextLinked    = "เชื่อมกับบัญชี %s แล้ว ✓" // %s = the user's ชื่อที่แสดง
	TextBadCode   = "ลิงก์หมดอายุ กดเชื่อมใหม่จากแอป"
	TextLinkFirst = "เชื่อมบัญชีก่อนนะครับ: แอป → โปรไฟล์ → Telegram"
	TextChatOff   = "ปิดการจดรายการไว้ เปิดได้ในแอป"
	TextUnlinked  = "ยกเลิกการเชื่อมแล้ว"
	TextLastLogin = "ตั้งรหัสผ่านในแอปก่อน ไม่งั้นจะเข้าบัญชีไม่ได้"

	// 1b-2 sign up / log in with Telegram
	TextLoginAsk       = "เข้าสู่ระบบ chubi-pocket เป็น %s ไหมครับ?"
	TextLoginNoAccount = "Telegram นี้ยังไม่มีบัญชี chubi-pocket"
	ButtonConfirm      = "✓ ยืนยัน"
	ButtonSignup       = "สร้างบัญชีใหม่"
	TextLoginExpired   = "ลิงก์หมดอายุ ลองใหม่จากแอป"
	TextLoginDone      = "ยืนยันแล้ว ✓ กลับไปที่แอปได้เลย"
	TextSignupDone     = "กลับไปที่แอปเพื่อสร้างบัญชีต่อได้เลย"

	// loginPrefix — the start payload of a login code (`/start login_<code>`).
	loginPrefix = "login_"
	// callback data: "li:<code id>" (log in as the linked account) / "su:<code id>" (sign up)
	cbLogin  = "li:"
	cbSignup = "su:"
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
	OutcomeStart     Outcome = "start"      // /start without a code from a linked chat
	OutcomeLinked    Outcome = "linked"     // /start <code>: bound
	OutcomeBadCode   Outcome = "bad_code"   // /start <code>: no such / used / expired code
	OutcomeUnlinked  Outcome = "unlinked"   // /unlink from a linked chat
	OutcomeLastLogin Outcome = "last_login" // /unlink or a move refused: the account's only way in
	OutcomeLoginAsk  Outcome = "login_ask"  // /start login_<code>: the confirm / sign-up button sent
	OutcomeLoginBad  Outcome = "login_bad"  // /start login_<code> or a button: code gone / not this chat's
	OutcomeLoginOK   Outcome = "login_ok"   // a button tapped: confirmed
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
	// TelegramBind — use a link code for this chat; ok false = bad, used or expired.
	TelegramBind(ctx context.Context, code string, chatID int64, account string) (displayName string, ok bool, err error)
	// TelegramUnlink — the chat unlinks itself; false = it wasn't linked.
	TelegramUnlink(ctx context.Context, chatID int64) (bool, error)
	// TelegramLoginOpen — /start login_<code> (1b-2): the chat takes the login code. linkedName =
	// the account this chat is linked to ("" = none: sign up). ok false = bad / used / expired.
	TelegramLoginOpen(ctx context.Context, code string, chatID int64, account, name string) (codeID uuid.UUID, linkedName string, ok bool, err error)
	// TelegramLoginConfirm — the chat tapped the button; ok false = gone or not this chat's.
	TelegramLoginConfirm(ctx context.Context, codeID uuid.UUID, chatID int64) (bool, error)
}

// Handler holds the bot's logic; the webhook only decodes and calls HandleUpdate.
type Handler struct {
	send     sender
	store    saver // nil = texts are not kept
	accounts Accounts
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

	if code, ok := command(m.Text, "/start"); ok && strings.HasPrefix(code, loginPrefix) {
		return h.loginOpen(ctx, u, m, strings.TrimPrefix(code, loginPrefix))
	}
	if code, ok := command(m.Text, "/start"); ok && code != "" {
		name, bound, err := h.accounts.TelegramBind(ctx, code, m.Chat.ID, accountLabel(m.From))
		if errors.Is(err, shared.ErrLastLoginMethod) {
			return h.reply(ctx, u, m, true, TextLastLogin, OutcomeLastLogin)
		}
		if err != nil {
			return h.failed(u, m, "link", err)
		}
		if !bound {
			return h.reply(ctx, u, m, false, TextBadCode, OutcomeBadCode)
		}
		return h.reply(ctx, u, m, true, strings.Replace(TextLinked, "%s", name, 1), OutcomeLinked)
	}

	userID, chatOn, linked, err := h.accounts.TelegramUser(ctx, m.Chat.ID)
	if err != nil {
		return h.failed(u, m, "lookup", err)
	}
	if _, ok := command(m.Text, "/unlink"); ok && linked {
		_, err := h.accounts.TelegramUnlink(ctx, m.Chat.ID)
		if errors.Is(err, shared.ErrLastLoginMethod) {
			return h.reply(ctx, u, m, true, TextLastLogin, OutcomeLastLogin)
		}
		if err != nil {
			return h.failed(u, m, "unlink", err)
		}
		return h.reply(ctx, u, m, false, TextUnlinked, OutcomeUnlinked)
	}
	switch {
	case !linked:
		return h.reply(ctx, u, m, false, TextLinkFirst, OutcomeLinkFirst)
	case isCommand(m.Text, "/start"):
		return h.reply(ctx, u, m, true, TextHello, OutcomeStart)
	case !chatOn:
		return h.reply(ctx, u, m, true, TextChatOff, OutcomeChatOff)
	case m.Text == "":
		return h.reply(ctx, u, m, true, TextNotText, OutcomeNotText)
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
	return h.reply(ctx, u, m, true, TextReceived, OutcomeReceived)
}

// loginOpen — /start login_<code> (1b-2): ask to confirm (linked chat) or offer sign-up.
func (h *Handler) loginOpen(ctx context.Context, u Update, m *Message, code string) Outcome {
	name := ""
	if m.From != nil {
		name = strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
	}
	codeID, linkedName, ok, err := h.accounts.TelegramLoginOpen(ctx, code, m.Chat.ID, accountLabel(m.From), name)
	if err != nil {
		return h.failed(u, m, "login open", err)
	}
	if !ok {
		return h.reply(ctx, u, m, false, TextLoginExpired, OutcomeLoginBad)
	}
	text, button := TextLoginNoAccount, Button{Text: ButtonSignup, Data: cbSignup + codeID.String()}
	if linkedName != "" {
		text, button = strings.Replace(TextLoginAsk, "%s", linkedName, 1), Button{Text: ButtonConfirm, Data: cbLogin + codeID.String()}
	}
	if err := h.send.SendButtons(ctx, m.Chat.ID, text, m.MessageID, []Button{button}); err != nil {
		h.log.Error("telegram: reply failed", "update_id", u.UpdateID, "chat_id", m.Chat.ID, "error", err)
	}
	h.logLine(u, m, linkedName != "", OutcomeLoginAsk)
	return OutcomeLoginAsk
}

// callback — a tap on [✓ ยืนยัน] / [สร้างบัญชีใหม่]: confirm the login code, edit the message.
func (h *Handler) callback(ctx context.Context, u Update, cq *CallbackQuery) Outcome {
	m := cq.Message
	done := ""
	var raw string
	switch {
	case strings.HasPrefix(cq.Data, cbLogin):
		done, raw = TextLoginDone, strings.TrimPrefix(cq.Data, cbLogin)
	case strings.HasPrefix(cq.Data, cbSignup):
		done, raw = TextSignupDone, strings.TrimPrefix(cq.Data, cbSignup)
	}
	codeID, perr := uuid.Parse(raw)
	ok := false
	if done != "" && perr == nil && cq.From.ID == m.Chat.ID {
		var err error
		if ok, err = h.accounts.TelegramLoginConfirm(ctx, codeID, m.Chat.ID); err != nil {
			_ = h.send.AnswerCallback(ctx, cq.ID, "")
			return h.failed(u, m, "login confirm", err)
		}
	}
	out, notice, edit := OutcomeLoginOK, "", done
	if !ok {
		out, notice, edit = OutcomeLoginBad, TextLoginExpired, TextLoginExpired
	}
	if err := h.send.AnswerCallback(ctx, cq.ID, notice); err != nil {
		h.log.Error("telegram: answer callback failed", "update_id", u.UpdateID, "error", err)
	}
	if err := h.send.EditText(ctx, m.Chat.ID, m.MessageID, edit); err != nil {
		h.log.Error("telegram: edit failed", "update_id", u.UpdateID, "error", err)
	}
	h.log.Info("telegram update", "update_id", u.UpdateID, "outcome", string(out), "chat_id", m.Chat.ID,
		"chat_type", m.Chat.Type, "kind", "callback")
	return out
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

// accountLabel — how the linked account shows in the app: "@username", else the first name.
func accountLabel(u *User) string {
	switch {
	case u == nil:
		return ""
	case u.Username != "":
		return "@" + u.Username
	}
	return u.FirstName
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
