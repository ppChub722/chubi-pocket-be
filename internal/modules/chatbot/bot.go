package chatbot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/telegram"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
)

// Sender is the part of the Telegram client the bot uses.
type Sender interface {
	SendHTML(ctx context.Context, chatID int64, text string, replyTo int64) error // text is Telegram HTML
	SendCard(ctx context.Context, chatID int64, text string, replyTo int64, buttons []telegram.Button) (int64, error)
	EditCard(ctx context.Context, chatID, messageID int64, text string, buttons []telegram.Button) error
	SendForceReply(ctx context.Context, chatID int64, text string, replyTo int64, placeholder string) (int64, error)
	AnswerCallback(ctx context.Context, callbackID, text string) error
	SetReaction(ctx context.Context, chatID, messageID int64, emoji string) error
	SendDraft(ctx context.Context, chatID, draftID int64, text string, canStop bool) error
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
	cbEdit   = "c:ed:"
	cbDone   = "c:dn" // the grey button of a card that is finished: tapping it only says so
)

// How long a fake "hang" waits between two "reading" drafts (Telegram drops a draft after about 30 s).
const draftRefresh = 20 * time.Second

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
	calls   *callSet // model calls in flight, by the draft the stop button belongs to

	// LogBodies logs each call's request (lists, date, the user's text) and the model's raw
	// answer. It writes the user's own words to the log: debug only (LLM_LOG_BODIES).
	LogBodies bool
}

func New(send Sender, lists ListSource, router *llm.Router, perMinute int, timeout time.Duration, log *slog.Logger) *Bot {
	return &Bot{send: send, lists: lists, router: router, cards: newMemCards(2000), limit: newMinuteLimit(perMinute),
		timeout: timeout, log: log, now: time.Now, calls: newCallSet()}
}

// callSet holds the cancel of each model call in flight, so the stop button can end one.
type callSet struct {
	mu sync.Mutex
	m  map[[2]int64]context.CancelFunc
}

func newCallSet() *callSet { return &callSet{m: map[[2]int64]context.CancelFunc{}} }

func (s *callSet) add(chatID, draftID int64, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[[2]int64{chatID, draftID}] = cancel
}

func (s *callSet) remove(chatID, draftID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, [2]int64{chatID, draftID})
}

// Stop ends the model call whose "reading" draft had its stop button pressed (nothing in flight = nothing happens).
func (b *Bot) Stop(chatID, draftID int64) {
	b.calls.mu.Lock()
	cancel := b.calls.m[[2]int64{chatID, draftID}]
	b.calls.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// WithSaver makes ✓ real (K4): rows for the app's pending page, or booked at once when the user turned auto on.
// Nothing is written before ✓. The fake provider never writes.
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
	if err := b.send.SendDraft(ctx, tr.chatID, tr.msgID, tr.t.Thinking, true); err != nil {
		b.log.Warn("chatbot: draft failed", "chat_id", tr.chatID, "error", err)
	}
	if r := m.ReplyToMessage; r != nil {
		if c, ok := b.cards.byMessage(tr.chatID, r.MessageID); ok {
			b.edit(ctx, tr, c, m.Text)
			return
		}
		if isCardMessage(r) { // a card the bot no longer holds (restart, or already edited): not a new message
			b.react(ctx, tr.chatID, tr.msgID, reactFail)
			b.say(ctx, tr.chatID, tr.t.ReplyClosed, tr.msgID)
			return
		}
	}
	if b.router.Fake() && strings.EqualFold(strings.TrimSpace(m.Text), "forget cards") { // as after a restart
		b.cards.forgetAll()
		b.say(ctx, tr.chatID, "forgot every card", tr.msgID)
		return
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

// isCardMessage: the message a Reply points at is one of the bot's cards (its first line holds the card head).
func isCardMessage(r *telegram.Message) bool {
	if r.From == nil || !r.From.IsBot {
		return false
	}
	first, _, _ := strings.Cut(r.Text, "\n")
	return strings.Contains(first, emojiCard)
}

// fresh — a new message: parse, then one card or one fixed line.
func (b *Bot) fresh(ctx context.Context, tr turn, text string) {
	lists, out, ok := b.parse(ctx, tr, text, nil)
	if !ok {
		return
	}
	if len(out.Items) == 0 {
		b.react(ctx, tr.chatID, tr.msgID, reactFail)
		b.sayFixed(ctx, tr.chatID, tr.label, tr.t.hint(out.Hint), tr.msgID)
		return
	}
	c := Card{ID: newCardID(), UserID: tr.userID, ChatID: tr.chatID, Items: out.Items, State: CardOpen,
		Label: tr.label, SourceID: tr.msgID, Lang: tr.lang}
	b.markMissing(&c)
	id, err := b.send.SendCard(ctx, tr.chatID, cardHTML(c, lists, b.today()), tr.msgID, buttons(c.ID, tr.t))
	if err != nil {
		b.log.Error("chatbot: send card failed", "chat_id", tr.chatID, "error", err)
		return
	}
	c.MessageID = id
	b.cards.put(&c)
	b.sayUnclear(ctx, tr, out.Unclear)
}

// sayUnclear answers the parts of a message whose intent is not clear, after the card of the clear ones.
func (b *Bot) sayUnclear(ctx context.Context, tr turn, parts []string) {
	if len(parts) == 0 {
		return
	}
	var named []string
	for _, p := range parts {
		if p != "" {
			named = append(named, p)
		}
	}
	if len(named) == 0 {
		b.sayFixed(ctx, tr.chatID, tr.label, tr.t.Unclear, tr.msgID)
		return
	}
	b.say(ctx, tr.chatID, tr.label+fmt.Sprintf(tr.t.UnclearPart, strings.Join(named, sep)), tr.msgID)
}

// edit — a Reply on a card: the card's items + the edit text → the whole card again, sent as a new
// card under the edit message; the old one keeps its items, loses its buttons and points down
// (owner 2026-11-04).
func (b *Bot) edit(ctx context.Context, tr turn, c Card, text string) {
	tr.label = c.Label
	if c.State != CardOpen {
		b.react(ctx, c.ChatID, tr.msgID, reactFail)
		b.say(ctx, c.ChatID, tr.t.ReplyClosed, tr.msgID)
		return
	}
	lists, out, ok := b.parse(ctx, tr, text, c.Items)
	if !ok {
		return
	}
	if len(out.Items) == 0 {
		b.react(ctx, c.ChatID, tr.msgID, reactFail)
		b.sayFixed(ctx, c.ChatID, c.Label, tr.t.hint(out.Hint), tr.msgID)
		return
	}
	before := c
	c.Items, c.Lang = out.Items, tr.lang
	b.markMissing(&c)
	id, err := b.send.SendCard(ctx, c.ChatID, cardHTML(c, lists, b.today()), tr.msgID, buttons(c.ID, tr.t))
	if err != nil {
		b.log.Error("chatbot: send card failed", "chat_id", c.ChatID, "error", err)
		return
	}
	c.MessageID = id
	b.cards.repoint(&c, before.MessageID)
	if err := b.send.EditCard(ctx, c.ChatID, before.MessageID, cardWith(before, lists, b.today(), tr.t.Edited), doneButtons(tr.t.ButtonEdited)); err != nil {
		b.log.Error("chatbot: edit card failed", "chat_id", c.ChatID, "error", err)
	}
	b.react(ctx, c.ChatID, tr.msgID, reactDone)
	b.sayUnclear(ctx, tr, out.Unclear)
}

func (b *Bot) parse(ctx context.Context, tr turn, text string, current []Item) (Lists, Outcome, bool) {
	lists, err := b.lists.Lists(ctx, tr.userID)
	if err != nil {
		b.fail(ctx, tr, CodeDB, "lists", err)
		return Lists{}, Outcome{}, false
	}
	pick := b.router.For(ctx, tr.userID)
	cctx, cancel := b.callContext(ctx, tr, text)
	defer cancel()
	b.calls.add(tr.chatID, tr.msgID, cancel)
	defer b.calls.remove(tr.chatID, tr.msgID)
	start := time.Now()
	out, err := Parse(cctx, pick.Client, b.today(), lists, text, current)
	ms := time.Since(start).Milliseconds()
	if err != nil && errors.Is(err, context.Canceled) && ctx.Err() == nil { // the stop button: not an error, not counted
		b.react(ctx, tr.chatID, tr.msgID, "")
		b.say(ctx, tr.chatID, tr.label+tr.t.Stopped, tr.msgID)
		return Lists{}, Outcome{}, false
	}
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

// callContext is the context of one model call. Its timeout is b.timeout; the fake mode's "hang" has none
// (and keeps its draft alive), so the stop button can be tried for as long as wanted.
func (b *Bot) callContext(ctx context.Context, tr turn, text string) (context.Context, context.CancelFunc) {
	if b.router.Fake() && strings.EqualFold(strings.TrimSpace(text), "hang") {
		cctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		go func() {
			for {
				select {
				case <-cctx.Done():
					return
				case <-time.After(draftRefresh):
					_ = b.send.SendDraft(cctx, tr.chatID, tr.msgID, tr.t.Thinking, true)
				}
			}
		}()
		return cctx, cancel
	}
	return context.WithTimeout(ctx, b.timeout)
}

// Tap handles a button of a card; false = not a card button (telegram handles it).
func (b *Bot) Tap(ctx context.Context, cq *telegram.CallbackQuery) bool {
	if !strings.HasPrefix(cq.Data, cbPrefix) || cq.Message == nil {
		return false
	}
	if cq.Data == cbDone { // the grey button of a finished card
		b.answer(ctx, cq.ID, textsFor(telegram.Lang("", cq.From.LanguageCode)).Handled)
		return true
	}
	if strings.HasPrefix(cq.Data, cbEdit) {
		return b.tapEdit(ctx, cq, strings.TrimPrefix(cq.Data, cbEdit))
	}
	state, id := CardSaved, strings.TrimPrefix(cq.Data, cbOK)
	if strings.HasPrefix(cq.Data, cbNo) {
		state, id = CardCancelled, strings.TrimPrefix(cq.Data, cbNo)
	}
	c, ok := b.cards.get(id)
	t := textsFor(telegram.Lang(c.Lang, cq.From.LanguageCode))
	if !ok && b.expired(ctx, cq, t) {
		return true
	}
	if !b.mine(c, ok, cq) {
		b.answer(ctx, cq.ID, t.Handled)
		return true
	}
	if state == CardSaved && b.saves() {
		if missing := b.missingNow(c); len(missing) > 0 { // not ready: say what is missing, the card stays open
			b.answer(ctx, cq.ID, t.missing(missing))
			return true
		}
	}
	if !b.cards.close(id, state) {
		b.answer(ctx, cq.ID, t.Handled)
		return true
	}
	done, reaction, toast := t.Saved, reactDone, fmt.Sprintf(t.ToastSaved, len(c.Items))
	if state == CardCancelled {
		done, reaction, toast = t.Cancelled, "", t.Cancelled
	}
	switch {
	case b.router.Fake():
		done += TextFakeMark
	case !b.saves():
		if state == CardSaved {
			done += t.TrialMark
		}
	case state == CardSaved:
		text, booked, ok := b.settle(ctx, c, t)
		if !ok { // nothing was booked: the card stays open, the user can press ✓ again
			b.cards.reopen(c.ID)
			b.answer(ctx, cq.ID, "")
			return true
		}
		done, toast = text, fmt.Sprintf(t.ToastSaved, booked)
		if text == t.Queued {
			toast = fmt.Sprintf(t.ToastQueued, booked)
		}
		if booked == 0 {
			reaction, toast = reactFail, ""
		}
	}
	b.answer(ctx, cq.ID, toast)
	lists, err := b.lists.Lists(ctx, c.UserID)
	if err != nil {
		b.log.Error("chatbot: lists failed", "chat_id", c.ChatID, "error", err)
	}
	label := t.Saved
	if state == CardCancelled {
		label = t.Cancelled
	}
	if err := b.send.EditCard(ctx, c.ChatID, c.MessageID, cardWith(c, lists, b.today(), done), doneButtons(label)); err != nil {
		b.log.Error("chatbot: edit card failed", "chat_id", c.ChatID, "error", err)
	}
	b.react(ctx, c.ChatID, c.SourceID, reaction)
	return true
}

// mine: the card exists and the tap came from the private chat it was sent to.
func (b *Bot) mine(c Card, found bool, cq *telegram.CallbackQuery) bool {
	return found && c.ChatID == cq.Message.Chat.ID && cq.From.ID == cq.Message.Chat.ID
}

// expired answers a tap on a card the bot no longer holds (a restart): says so and takes the buttons off.
// false = the tap is not from the chat itself, so the caller treats it as any foreign tap.
func (b *Bot) expired(ctx context.Context, cq *telegram.CallbackQuery, t Texts) bool {
	if cq.From.ID != cq.Message.Chat.ID {
		return false
	}
	b.answer(ctx, cq.ID, t.Expired)
	text := html.EscapeString(cq.Message.Text) + "\n\n" + html.EscapeString(t.Expired)
	if err := b.send.EditCard(ctx, cq.Message.Chat.ID, cq.Message.MessageID, text, nil); err != nil {
		b.log.Error("chatbot: edit expired card failed", "chat_id", cq.Message.Chat.ID, "error", err)
	}
	return true
}

// tapEdit — ✏️: opens the reply box on the user's side (a force reply with a placeholder); what they send
// there is an edit of this card, the same as a Reply on the card.
func (b *Bot) tapEdit(ctx context.Context, cq *telegram.CallbackQuery, id string) bool {
	c, ok := b.cards.get(id)
	t := textsFor(telegram.Lang(c.Lang, cq.From.LanguageCode))
	if !ok && b.expired(ctx, cq, t) {
		return true
	}
	if !b.mine(c, ok, cq) || c.State != CardOpen {
		b.answer(ctx, cq.ID, t.Handled)
		return true
	}
	b.answer(ctx, cq.ID, "")
	prompt, err := b.send.SendForceReply(ctx, c.ChatID, t.EditPrompt, c.MessageID, t.EditPlaceholder)
	if err != nil {
		b.log.Error("chatbot: edit prompt failed", "chat_id", c.ChatID, "error", err)
		return true
	}
	b.cards.alias(c.ChatID, prompt, c.ID)
	return true
}

// settle is ✓ on a real card: not auto → the rows wait on the pending page; auto → they are booked
// at once. ok false = nothing happened (an error line was sent and the card stays open). A booking that
// only partly went through closes the card and says how many.
func (b *Bot) settle(ctx context.Context, c Card, t Texts) (text string, booked int, ok bool) {
	ref := SaveRef{ChatID: c.ChatID, CardID: c.ID, MessageID: c.SourceID}
	if !b.saver.Auto(ctx, c.UserID, groupTx) {
		if err := b.saver.Queue(ctx, c.UserID, ref, c.Items); err != nil {
			b.settleFailed(ctx, c, t, "queue", err)
			return "", 0, false
		}
		return t.Queued, len(c.Items), true
	}
	out, err := b.saver.Book(ctx, c.UserID, ref, c.Items)
	switch {
	case err == nil:
		if out.Unlinked > 0 { // booked, but not tied to the recurring slot
			return t.Saved + "\n" + t.Unlinked, out.Booked, true
		}
		return t.Saved, out.Booked, true
	case out.Booked > 0:
		b.log.Error("chatbot: booked only part", "chat_id", c.ChatID, "booked", out.Booked, "of", len(c.Items), "error", err)
		return fmt.Sprintf(t.PartialBooked, out.Booked, len(c.Items)), out.Booked, true
	}
	b.settleFailed(ctx, c, t, "book", err)
	return "", 0, false
}

// settleFailed tells the user (a fixed line + code, only owners reach cards) and logs; no row is left behind.
func (b *Bot) settleFailed(ctx context.Context, c Card, t Texts, stage string, err error) {
	ref := newCardID()[:4]
	var be *pending.BookError
	if errors.As(err, &be) && be.Code == "INSUFFICIENT_SAVING" { // the saving it comes from holds less
		b.log.Warn("chatbot: ✓ refused", "chat_id", c.ChatID, "code", be.Code, "ref", ref)
		b.say(ctx, c.ChatID, c.Label+t.InsufficientSaving, c.SourceID)
		return
	}
	code := CodeDB
	if errors.As(err, &be) {
		code = "E-BOOK " + be.Code
	}
	b.log.Error("chatbot: ✓ failed", "chat_id", c.ChatID, "code", code, "ref", ref, "stage", stage, "error", err)
	b.say(ctx, c.ChatID, c.Label+t.fail(CodeDB)+"\n"+code+" #"+ref, c.SourceID)
}

// markMissing runs the pending page's readiness check on the card's items before the card goes out:
// a field that is missing shows as "[… ?]" and ✓ will not go until the user fills it by Reply.
func (b *Bot) markMissing(c *Card) {
	if !b.saves() {
		return
	}
	for i, codes := range b.saver.Check(c.Items) {
		for _, code := range codes {
			if f := unsureField(code); f != "" && !contains(c.Items[i].Unsure, f) {
				c.Items[i].Unsure = append(c.Items[i].Unsure, f)
			}
		}
	}
}

// missingNow: the distinct things the card's items still lack (checked again at ✓).
func (b *Bot) missingNow(c Card) []string {
	var out []string
	for _, codes := range b.saver.Check(c.Items) {
		for _, code := range codes {
			if !contains(out, code) {
				out = append(out, code)
			}
		}
	}
	return out
}

// unsureField: the card field a readiness code is shown on.
func unsureField(code string) string {
	switch code {
	case "wallet", "wallet_to":
		return "account"
	case "amount", "date", "type":
		return code
	}
	return ""
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
	return []telegram.Button{
		{Text: t.ButtonOK, Data: cbOK + id, Style: "success"},
		{Text: t.ButtonCancel, Data: cbNo + id, Style: "danger"},
		{Text: t.ButtonEdit, Data: cbEdit + id},
	}
}

// doneButtons: the one grey button a finished card keeps in place of its buttons.
func doneButtons(label string) []telegram.Button {
	return []telegram.Button{{Text: label, Data: cbDone}}
}

// cardWith is the card as HTML with a fixed line (what happened to it) under it.
func cardWith(c Card, lists Lists, today time.Time, line string) string {
	return cardHTML(c, lists, today) + "\n\n" + html.EscapeString(line)
}

// say sends a plain line (escaped: it may hold the user's own words).
func (b *Bot) say(ctx context.Context, chatID int64, text string, replyTo int64) {
	b.sayHTML(ctx, chatID, html.EscapeString(text), replyTo)
}

// sayFixed sends one of the bot's own lines; an example in it goes in monospace.
func (b *Bot) sayFixed(ctx context.Context, chatID int64, label, text string, replyTo int64) {
	b.sayHTML(ctx, chatID, html.EscapeString(label)+htmlFixed(text), replyTo)
}

func (b *Bot) sayHTML(ctx context.Context, chatID int64, text string, replyTo int64) {
	if err := b.send.SendHTML(ctx, chatID, text, replyTo); err != nil {
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
