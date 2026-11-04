package chatbot

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/telegram"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
)

// Sender is the part of the Telegram client the bot uses.
type Sender interface {
	SendMessage(ctx context.Context, chatID int64, text string, replyTo int64) error
	SendCard(ctx context.Context, chatID int64, text string, replyTo int64, buttons []telegram.Button) (int64, error)
	EditCard(ctx context.Context, chatID, messageID int64, text string, buttons []telegram.Button) error
	AnswerCallback(ctx context.Context, callbackID, text string) error
	SetReaction(ctx context.Context, chatID, messageID int64, emoji string) error
	SendDraft(ctx context.Context, chatID, draftID int64, text string) error
}

// ListSource reads the user's wallets and categories.
type ListSource interface {
	Lists(ctx context.Context, userID uuid.UUID) (Lists, error)
}

const (
	cbPrefix = "c:"
	cbOK     = "c:ok:"
	cbNo     = "c:no:"
)

var bangkok = time.FixedZone("ICT", 7*3600)

type Bot struct {
	send    Sender
	lists   ListSource
	router  *llm.Router
	cards   *memCards
	limit   *minuteLimit
	timeout time.Duration
	log     *slog.Logger
	now     func() time.Time
}

func New(send Sender, lists ListSource, router *llm.Router, perMinute int, timeout time.Duration, log *slog.Logger) *Bot {
	return &Bot{send: send, lists: lists, router: router, cards: newMemCards(2000), limit: newMinuteLimit(perMinute),
		timeout: timeout, log: log, now: time.Now}
}

func (b *Bot) today() time.Time {
	n := b.now().In(bangkok)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// Text handles a text from a linked chat with the chat permission on (telegram checked both).
func (b *Bot) Text(ctx context.Context, userID uuid.UUID, m *telegram.Message) {
	chatID := m.Chat.ID
	if utf8.RuneCountInString(m.Text) > MaxInput {
		b.say(ctx, chatID, TextTooLong, m.MessageID)
		return
	}
	if !b.limit.allow(userID, b.now()) {
		b.say(ctx, chatID, TextTooFast, m.MessageID)
		return
	}
	b.react(ctx, chatID, m.MessageID, reactRead)
	if r := m.ReplyToMessage; r != nil {
		if c, ok := b.cards.byMessage(chatID, r.MessageID); ok {
			b.edit(ctx, c, m)
			return
		}
	}
	// The draft bubble goes when the card (or a line) is sent; an edit sends nothing new, so only
	// new messages get one.
	if err := b.send.SendDraft(ctx, chatID, m.MessageID, TextThinking); err != nil {
		b.log.Warn("chatbot: draft failed", "chat_id", chatID, "error", err)
	}
	if b.router.Fake() && strings.EqualFold(strings.TrimSpace(m.Text), "all") {
		for _, kw := range llm.FakeKeywords {
			b.fresh(ctx, userID, chatID, m.MessageID, kw, "["+kw+"] ")
		}
		return
	}
	b.fresh(ctx, userID, chatID, m.MessageID, m.Text, "")
}

// fresh — a new message: parse, then one card or one fixed line.
func (b *Bot) fresh(ctx context.Context, userID uuid.UUID, chatID, msgID int64, text, label string) {
	lists, out, ok := b.parse(ctx, userID, chatID, msgID, text, nil, label)
	if !ok {
		return
	}
	if len(out.Items) == 0 {
		b.react(ctx, chatID, msgID, reactFail)
		b.say(ctx, chatID, label+hintText(out.Hint), msgID)
		return
	}
	c := Card{ID: newCardID(), UserID: userID, ChatID: chatID, Items: out.Items, State: CardOpen, Label: label,
		SourceID: msgID}
	id, err := b.send.SendCard(ctx, chatID, cardText(c, lists, b.today()), msgID, buttons(c.ID))
	if err != nil {
		b.log.Error("chatbot: send card failed", "chat_id", chatID, "error", err)
		return
	}
	c.MessageID = id
	b.cards.put(&c)
}

// edit — a Reply on a card: the card's items + the edit text → the whole card again.
func (b *Bot) edit(ctx context.Context, c Card, m *telegram.Message) {
	if c.State != CardOpen {
		b.say(ctx, c.ChatID, TextHandled, m.MessageID)
		return
	}
	lists, out, ok := b.parse(ctx, c.UserID, c.ChatID, m.MessageID, m.Text, c.Items, c.Label)
	if !ok {
		return
	}
	if len(out.Items) == 0 {
		b.react(ctx, c.ChatID, m.MessageID, reactFail)
		b.say(ctx, c.ChatID, c.Label+hintText(out.Hint), m.MessageID)
		return
	}
	c.Items = out.Items
	b.cards.put(&c)
	if err := b.send.EditCard(ctx, c.ChatID, c.MessageID, cardText(c, lists, b.today()), buttons(c.ID)); err != nil {
		b.log.Error("chatbot: edit card failed", "chat_id", c.ChatID, "error", err)
		return
	}
	b.react(ctx, c.ChatID, m.MessageID, reactDone)
}

func (b *Bot) parse(ctx context.Context, userID uuid.UUID, chatID, msgID int64, text string, current []Item, label string) (Lists, Outcome, bool) {
	lists, err := b.lists.Lists(ctx, userID)
	if err != nil {
		b.fail(ctx, chatID, msgID, label, CodeDB, "lists", err)
		return Lists{}, Outcome{}, false
	}
	pick := b.router.For(ctx, userID)
	cctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	start := time.Now()
	out, err := Parse(cctx, pick.Client, b.today(), lists, text, current)
	ms := time.Since(start).Milliseconds()
	if errors.Is(err, llm.ErrOff) {
		b.say(ctx, chatID, label+TextOff, msgID)
		return Lists{}, Outcome{}, false
	}
	if err != nil {
		b.fail(ctx, chatID, msgID, label, llm.ErrorCode(err), "llm", err, "provider", pick.Provider, "ms", ms)
		return Lists{}, Outcome{}, false
	}
	hint := ""
	if len(out.Items) == 0 && out.Fail == "" {
		hint = out.Hint
	}
	b.log.Info("chatbot: parsed", "chat_id", chatID, "user_id", userID, "provider", pick.Provider, "model", out.Model,
		"edit", current != nil, "items", len(out.Items), "hint", hint, "fail", out.Fail, "ms", ms,
		"categories", pickedCategories(out.Items, lists), "model_picks", out.Picks, "unsure", unsureFieldsOf(out.Items),
		"tokens_in", out.Usage.In, "tokens_out", out.Usage.Out,
		"cache_read", out.Usage.CacheRead, "cache_write", out.Usage.CacheWrite)
	if out.Fail != "" {
		b.fail(ctx, chatID, msgID, label, out.Fail, "answer", nil)
		return Lists{}, Outcome{}, false
	}
	return lists, out, true
}

// Tap handles a button of a card; false = not a card button (telegram handles it).
func (b *Bot) Tap(ctx context.Context, cq *telegram.CallbackQuery) bool {
	if !strings.HasPrefix(cq.Data, cbPrefix) || cq.Message == nil {
		return false
	}
	state, id := CardSaved, strings.TrimPrefix(cq.Data, cbOK)
	if strings.HasPrefix(cq.Data, cbNo) {
		state, id = CardCancelled, strings.TrimPrefix(cq.Data, cbNo)
	}
	c, ok := b.cards.get(id)
	mine := ok && c.ChatID == cq.Message.Chat.ID && cq.From.ID == cq.Message.Chat.ID
	if !mine || !b.cards.close(id, state) {
		b.answer(ctx, cq.ID, TextHandled)
		return true
	}
	b.answer(ctx, cq.ID, "")
	lists, err := b.lists.Lists(ctx, c.UserID)
	if err != nil {
		b.log.Error("chatbot: lists failed", "chat_id", c.ChatID, "error", err)
	}
	done := TextSaved
	if state == CardCancelled {
		done = TextCancelled
	}
	// Cards live in memory only until pending rows are written (K4).
	switch {
	case b.router.Fake():
		done += TextFakeMark
	case state == CardSaved:
		done += TextTrialMark
	}
	if err := b.send.EditCard(ctx, c.ChatID, c.MessageID, cardText(c, lists, b.today())+"\n\n"+done, nil); err != nil {
		b.log.Error("chatbot: edit card failed", "chat_id", c.ChatID, "error", err)
	}
	if state == CardSaved {
		b.react(ctx, c.ChatID, c.SourceID, reactDone)
	} else {
		b.react(ctx, c.ChatID, c.SourceID, "")
	}
	return true
}

// fail logs a failure with a code and a short ref, and tells the user whose side it is. Until the
// user tier exists everyone also gets the code line (later: the owner only).
func (b *Bot) fail(ctx context.Context, chatID, msgID int64, label, code, stage string, err error, kv ...any) {
	ref := newCardID()[:4]
	args := append([]any{"chat_id", chatID, "code", code, "ref", ref, "stage", stage}, kv...)
	if err != nil {
		args = append(args, "error", err)
	}
	b.log.Warn("chatbot: failed", args...)
	b.react(ctx, chatID, msgID, reactFail)
	b.say(ctx, chatID, label+failText(code)+"\n"+code+" #"+ref, msgID)
}

// react sets the bot's reaction on the user's message; a failure only logs (it is decoration).
func (b *Bot) react(ctx context.Context, chatID, msgID int64, emoji string) {
	if msgID == 0 {
		return
	}
	if err := b.send.SetReaction(ctx, chatID, msgID, emoji); err != nil {
		b.log.Warn("chatbot: reaction failed", "chat_id", chatID, "error", err)
	}
}

// pickedCategories: the category name per item ("-" none), for the log; never the user's text.
func pickedCategories(items []Item, lists Lists) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		name := "-"
		if it.CategoryID != nil {
			for _, c := range lists.Categories {
				if c.ID == *it.CategoryID {
					name = c.Name
				}
			}
		}
		out = append(out, name)
	}
	return out
}

func unsureFieldsOf(items []Item) []string {
	var out []string
	for _, it := range items {
		for _, f := range it.Unsure {
			if !contains(out, f) {
				out = append(out, f)
			}
		}
	}
	return out
}

func buttons(id string) []telegram.Button {
	return []telegram.Button{{Text: ButtonOK, Data: cbOK + id}, {Text: ButtonCancel, Data: cbNo + id}}
}

func (b *Bot) say(ctx context.Context, chatID int64, text string, replyTo int64) {
	if err := b.send.SendMessage(ctx, chatID, text, replyTo); err != nil {
		b.log.Error("chatbot: reply failed", "chat_id", chatID, "error", err)
	}
}

func (b *Bot) answer(ctx context.Context, callbackID, text string) {
	if err := b.send.AnswerCallback(ctx, callbackID, text); err != nil {
		b.log.Error("chatbot: answer callback failed", "error", err)
	}
}

// minuteLimit allows perMinute messages per user in any rolling minute.
type minuteLimit struct {
	mu   sync.Mutex
	max  int
	hits map[uuid.UUID][]time.Time
}

func newMinuteLimit(max int) *minuteLimit {
	return &minuteLimit{max: max, hits: map[uuid.UUID][]time.Time{}}
}

func (l *minuteLimit) allow(user uuid.UUID, now time.Time) bool {
	if l.max <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	keep := l.hits[user][:0]
	for _, t := range l.hits[user] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.max {
		l.hits[user] = keep
		return false
	}
	l.hits[user] = append(keep, now)
	return true
}
