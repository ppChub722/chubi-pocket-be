package chatbot

import (
	"context"
	"errors"
	"fmt"
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

// ListSource reads the user's wallets, categories and app language.
type ListSource interface {
	Lists(ctx context.Context, userID uuid.UUID) (Lists, error)
	Lang(ctx context.Context, userID uuid.UUID) string
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
	saver   Saver // nil = nothing is written (fake mode, tests)
	meter   Meter // nil = no tier check, cap or call log
	cap     int   // model calls per user per month; 0 = no cap
	limit   *minuteLimit
	timeout time.Duration
	log     *slog.Logger
	now     func() time.Time

	// LogBodies logs each call's request (lists, date, the user's text) and the model's raw
	// answer. It writes the user's own words to the log: debug only (LLM_LOG_BODIES).
	LogBodies bool
}

func New(send Sender, lists ListSource, router *llm.Router, perMinute int, timeout time.Duration, log *slog.Logger) *Bot {
	return &Bot{send: send, lists: lists, router: router, cards: newMemCards(2000), limit: newMinuteLimit(perMinute),
		timeout: timeout, log: log, now: time.Now}
}

// WithSaver makes cards real: rows in the app's pending page (K4). The fake provider never writes.
func (b *Bot) WithSaver(s Saver) { b.saver = s }

func (b *Bot) saves() bool { return b.saver != nil && !b.router.Fake() }

// WithMeter turns on the tier check, the monthly cap and the call log (K2). The fake provider skips all three.
func (b *Bot) WithMeter(m Meter, monthlyCap int) { b.meter, b.cap = m, monthlyCap }

func (b *Bot) metering() bool { return b.meter != nil && !b.router.Fake() }

func (b *Bot) today() time.Time {
	n := b.now().In(bangkok)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// turn is one message being handled: who, where, and in which language.
type turn struct {
	userID uuid.UUID
	chatID int64
	msgID  int64
	lang   string
	t      Texts
	label  string // "[keyword] " in the fake's all mode
	owner  bool   // may see error codes (tier owner, or no tiers: fake, tests)
}

// Text handles a text from a linked chat with the chat permission on (telegram checked both).
func (b *Bot) Text(ctx context.Context, userID uuid.UUID, m *telegram.Message) {
	tg := ""
	if m.From != nil {
		tg = m.From.LanguageCode
	}
	lang := telegram.Lang(b.lists.Lang(ctx, userID), tg)
	tr := turn{userID: userID, chatID: m.Chat.ID, msgID: m.MessageID, lang: lang, t: textsFor(lang)}
	if utf8.RuneCountInString(m.Text) > MaxInput {
		b.say(ctx, tr.chatID, tr.t.TooLong, tr.msgID)
		return
	}
	tr.owner = true
	if b.metering() {
		tier, err := b.meter.Tier(ctx, userID)
		if err != nil {
			tr.owner = false
			b.fail(ctx, tr, CodeDB, "tier", err)
			return
		}
		if tier != TierOwner {
			b.say(ctx, tr.chatID, tr.t.TierFree, tr.msgID)
			return
		}
		if b.cap > 0 {
			used, err := b.meter.Used(ctx, userID, monthStart(b.now()))
			if err != nil {
				b.fail(ctx, tr, CodeDB, "cap", err)
				return
			}
			if used >= b.cap {
				b.say(ctx, tr.chatID, tr.t.CapReached, tr.msgID)
				return
			}
		}
	}
	if !b.limit.allow(userID, b.now()) {
		b.say(ctx, tr.chatID, tr.t.TooFast, tr.msgID)
		return
	}
	b.react(ctx, tr.chatID, tr.msgID, reactRead)
	// The draft bubble goes when the card (or a line) is sent.
	if err := b.send.SendDraft(ctx, tr.chatID, tr.msgID, tr.t.Thinking); err != nil {
		b.log.Warn("chatbot: draft failed", "chat_id", tr.chatID, "error", err)
	}
	if r := m.ReplyToMessage; r != nil {
		if c, ok := b.cards.byMessage(tr.chatID, r.MessageID); ok {
			b.edit(ctx, tr, c, m.Text)
			return
		}
	}
	if b.router.Fake() && strings.EqualFold(strings.TrimSpace(m.Text), "all") {
		for _, kw := range llm.FakeKeywords {
			kt := tr
			kt.label = "[" + kw + "] "
			b.fresh(ctx, kt, kw)
		}
		return
	}
	b.fresh(ctx, tr, m.Text)
}

// fresh — a new message: parse, then one card or one fixed line.
func (b *Bot) fresh(ctx context.Context, tr turn, text string) {
	lists, out, ok := b.parse(ctx, tr, text, nil)
	if !ok {
		return
	}
	if len(out.Items) == 0 {
		b.react(ctx, tr.chatID, tr.msgID, reactFail)
		b.say(ctx, tr.chatID, tr.label+tr.t.hint(out.Hint), tr.msgID)
		return
	}
	c := Card{ID: newCardID(), UserID: tr.userID, ChatID: tr.chatID, Items: out.Items, State: CardOpen,
		Label: tr.label, SourceID: tr.msgID, Lang: tr.lang}
	if b.saves() {
		ids, err := b.saver.Save(ctx, tr.userID, SaveRef{ChatID: tr.chatID, CardID: c.ID, MessageID: tr.msgID, Text: text}, out.Items)
		if err != nil {
			b.fail(ctx, tr, CodeDB, "save", err)
			return
		}
		c.PendingIDs = ids
	}
	id, err := b.send.SendCard(ctx, tr.chatID, cardText(c, lists, b.today()), tr.msgID, buttons(c.ID, tr.t))
	if err != nil {
		b.log.Error("chatbot: send card failed", "chat_id", tr.chatID, "error", err)
		b.drop(ctx, c)
		return
	}
	c.MessageID = id
	b.cards.put(&c)
}

// edit — a Reply on a card: the card's items + the edit text → the whole card again, sent as a new
// card under the edit message; the old one keeps its items, loses its buttons and points down
// (owner 2026-11-04).
func (b *Bot) edit(ctx context.Context, tr turn, c Card, text string) {
	tr.label = c.Label
	if c.State != CardOpen {
		b.say(ctx, c.ChatID, tr.t.Handled, tr.msgID)
		return
	}
	if b.saves() {
		if n, err := b.saver.Alive(ctx, c.UserID, c.PendingIDs); err != nil {
			b.fail(ctx, tr, CodeDB, "alive", err)
			return
		} else if n < len(c.PendingIDs) { // settled in the app: the card is stale
			b.cards.close(c.ID, CardCancelled)
			b.say(ctx, c.ChatID, tr.t.Handled, tr.msgID)
			return
		}
	}
	lists, out, ok := b.parse(ctx, tr, text, c.Items)
	if !ok {
		return
	}
	if len(out.Items) == 0 {
		b.react(ctx, c.ChatID, tr.msgID, reactFail)
		b.say(ctx, c.ChatID, c.Label+tr.t.hint(out.Hint), tr.msgID)
		return
	}
	before := c
	c.Items, c.Lang = out.Items, tr.lang
	if b.saves() {
		ids, err := b.saver.Replace(ctx, c.UserID, c.PendingIDs, SaveRef{ChatID: c.ChatID, CardID: c.ID, MessageID: c.SourceID, Text: text}, out.Items)
		if err != nil {
			b.fail(ctx, tr, CodeDB, "save", err)
			return
		}
		c.PendingIDs = ids
	}
	id, err := b.send.SendCard(ctx, c.ChatID, cardText(c, lists, b.today()), tr.msgID, buttons(c.ID, tr.t))
	if err != nil {
		b.log.Error("chatbot: send card failed", "chat_id", c.ChatID, "error", err)
		return
	}
	c.MessageID = id
	b.cards.repoint(&c, before.MessageID)
	if err := b.send.EditCard(ctx, c.ChatID, before.MessageID, cardText(before, lists, b.today())+"\n\n"+tr.t.Edited, nil); err != nil {
		b.log.Error("chatbot: edit card failed", "chat_id", c.ChatID, "error", err)
	}
	b.react(ctx, c.ChatID, tr.msgID, reactDone)
}

func (b *Bot) parse(ctx context.Context, tr turn, text string, current []Item) (Lists, Outcome, bool) {
	lists, err := b.lists.Lists(ctx, tr.userID)
	if err != nil {
		b.fail(ctx, tr, CodeDB, "lists", err)
		return Lists{}, Outcome{}, false
	}
	pick := b.router.For(ctx, tr.userID)
	cctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	start := time.Now()
	out, err := Parse(cctx, pick.Client, b.today(), lists, text, current)
	ms := time.Since(start).Milliseconds()
	if b.metering() && !errors.Is(err, llm.ErrOff) {
		b.record(ctx, tr, pick, out, err, ms)
	}
	if b.LogBodies {
		b.log.Info("chatbot: io", "chat_id", tr.chatID, "sent", out.Sent, "received", out.Raw)
	}
	if errors.Is(err, llm.ErrOff) {
		b.say(ctx, tr.chatID, tr.label+tr.t.Off, tr.msgID)
		return Lists{}, Outcome{}, false
	}
	if err != nil {
		b.fail(ctx, tr, llm.ErrorCode(err), "llm", err, "provider", pick.Provider, "ms", ms)
		return Lists{}, Outcome{}, false
	}
	hint := ""
	if len(out.Items) == 0 && out.Fail == "" {
		hint = out.Hint
	}
	b.log.Info("chatbot: parsed", "chat_id", tr.chatID, "user_id", tr.userID, "lang", tr.lang,
		"provider", pick.Provider, "model", out.Model,
		"edit", current != nil, "items", len(out.Items), "hint", hint, "fail", out.Fail, "ms", ms,
		"categories", pickedCategories(out.Items, lists), "model_picks", out.Picks, "unsure", unsureOf(out.Items),
		"tokens_in", out.Usage.In, "tokens_out", out.Usage.Out,
		"cache_read", out.Usage.CacheRead, "cache_write", out.Usage.CacheWrite)
	if out.Fail != "" {
		b.fail(ctx, tr, out.Fail, "answer", nil)
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
	t := textsFor(telegram.Lang(c.Lang, cq.From.LanguageCode))
	mine := ok && c.ChatID == cq.Message.Chat.ID && cq.From.ID == cq.Message.Chat.ID
	if mine && b.saves() { // rows settled in the app: the card is stale
		if n, err := b.saver.Alive(ctx, c.UserID, c.PendingIDs); err == nil && n < len(c.PendingIDs) {
			b.cards.close(id, CardCancelled)
			mine = false
		}
	}
	if !mine || !b.cards.close(id, state) {
		b.answer(ctx, cq.ID, t.Handled)
		return true
	}
	b.answer(ctx, cq.ID, "")
	lists, err := b.lists.Lists(ctx, c.UserID)
	if err != nil {
		b.log.Error("chatbot: lists failed", "chat_id", c.ChatID, "error", err)
	}
	done := t.Saved
	if state == CardCancelled {
		done = t.Cancelled
	}
	saved := -1 // rows booked; -1 = nothing was tried
	switch {
	case b.router.Fake():
		done += TextFakeMark
	case !b.saves():
		if state == CardSaved {
			done += t.TrialMark
		}
	case state == CardSaved:
		n, err := b.saver.Confirm(ctx, c.UserID, c.PendingIDs)
		if err != nil {
			b.log.Error("chatbot: confirm failed", "chat_id", c.ChatID, "error", err)
		}
		saved = n
		if n < len(c.PendingIDs) {
			done = fmt.Sprintf(t.Partial, n, len(c.PendingIDs))
		}
	default:
		if err := b.saver.Discard(ctx, c.UserID, c.PendingIDs); err != nil {
			b.log.Error("chatbot: discard failed", "chat_id", c.ChatID, "error", err)
		}
	}
	if err := b.send.EditCard(ctx, c.ChatID, c.MessageID, cardText(c, lists, b.today())+"\n\n"+done, nil); err != nil {
		b.log.Error("chatbot: edit card failed", "chat_id", c.ChatID, "error", err)
	}
	if state == CardSaved && saved == 0 {
		b.react(ctx, c.ChatID, c.SourceID, reactFail)
	} else if state == CardSaved {
		b.react(ctx, c.ChatID, c.SourceID, reactDone)
	} else {
		b.react(ctx, c.ChatID, c.SourceID, "")
	}
	return true
}

// fail logs a failure with a code and a short ref, and tells the user whose side it is; the code
// line is for the owner tier only.
func (b *Bot) fail(ctx context.Context, tr turn, code, stage string, err error, kv ...any) {
	ref := newCardID()[:4]
	args := append([]any{"chat_id", tr.chatID, "code", code, "ref", ref, "stage", stage}, kv...)
	if err != nil {
		args = append(args, "error", err)
	}
	b.log.Warn("chatbot: failed", args...)
	b.react(ctx, tr.chatID, tr.msgID, reactFail)
	b.say(ctx, tr.chatID, tr.label+tr.t.fail(code)+"\n"+code+" #"+ref, tr.msgID)
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

// unsureOf: per item, the fields the model guessed ("amount date"), for the log.
func unsureOf(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, strings.Join(it.Unsure, " "))
	}
	return out
}

func buttons(id string, t Texts) []telegram.Button {
	return []telegram.Button{{Text: t.ButtonOK, Data: cbOK + id}, {Text: t.ButtonCancel, Data: cbNo + id}}
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

// drop discards the rows of a card that never reached Telegram.
func (b *Bot) drop(ctx context.Context, c Card) {
	if !b.saves() || len(c.PendingIDs) == 0 {
		return
	}
	if err := b.saver.Discard(ctx, c.UserID, c.PendingIDs); err != nil {
		b.log.Error("chatbot: discard failed", "chat_id", c.ChatID, "error", err)
	}
}

// record logs one model call (tokens, ms, status; never the text). A failure only logs.
func (b *Bot) record(ctx context.Context, tr turn, pick llm.Pick, out Outcome, err error, ms int64) {
	status := "ok"
	switch {
	case err != nil:
		status = llm.ErrorCode(err)
	case out.Fail != "":
		status = out.Fail
	}
	model := out.Model
	if model == "" {
		model = pick.Model
	}
	c := Call{Channel: "telegram", Provider: pick.Provider, Model: model, Status: status, Ms: ms,
		In: out.Usage.In, Out: out.Usage.Out, CacheRead: out.Usage.CacheRead, CacheWrite: out.Usage.CacheWrite}
	if rerr := b.meter.Record(ctx, tr.userID, c); rerr != nil {
		b.log.Warn("chatbot: usage not logged", "chat_id", tr.chatID, "error", rerr)
	}
}
